//! Tag-query fact extraction for the [`Extractor::Tags`](super::Extractor) packs.
//!
//! One parse, one pass of the pack's tag query and one cursor walk per file:
//! - `@definition.*` matches become symbols. Their containers come from source nesting,
//!   which also yields the dotted qualified name, depth and locality; a function whose
//!   container is a type or module ([`METHOD_OWNERS`]) is a method;
//! - every identifier node of the pack's `ident_kinds`, and every `@reference.*` node,
//!   that is not a declaration name becomes a reference. Comments and string literals
//!   hold no identifier nodes, so prose and literals never become references;
//! - `@import.*` matches become imports, and identifiers inside them are import
//!   references.

use std::collections::{HashMap, HashSet};

use streaming_iterator::StreamingIterator;
use tree_sitter::{Node, Parser, QueryCursor};

use super::{Lang, METHOD_OWNERS, fact_kind, tag_query};
use crate::index::extract::{
    MAX_DOC_DEPTH, MAX_REFS_PER_FILE, MAX_SYMBOLS_PER_FILE, decl_end_line, doc_start_line, qualify,
    signature_hash,
};
use crate::index::facts::{FileFacts, ImportFact, RefFact, SymbolFact, flow, ref_kind};

/// Node kinds that put their descendants on the return path.
const RETURN_KINDS: &[&str] = &["return_statement", "return_expression", "return"];

/// Longest declaration header (source before the name) read by the export rule.
const MAX_HEADER_BYTES: usize = 512;

struct Def<'t> {
    node: Node<'t>,
    name: Node<'t>,
    /// The `@params` list, or the declaration's own `parameter` children for grammars
    /// without a list node (Swift).
    params: Vec<Node<'t>>,
    /// `FileFacts` kind of the `@definition.<label>`.
    kind: &'static str,
}

fn list_arity(list: Node<'_>) -> u32 {
    let mut c = list.walk();
    list.named_children(&mut c)
        .filter(|n| !n.kind().contains("comment"))
        .count() as u32
}

/// Reference kind for a `@reference.<tag>`.
fn ref_tag(tag: &str) -> Option<u8> {
    Some(match tag {
        "call" => ref_kind::CALL,
        "member_call" => ref_kind::MEMBER_CALL,
        "member" => ref_kind::MEMBER,
        "type" => ref_kind::TYPE,
        "extends" => ref_kind::EXTENDS,
        "implements" => ref_kind::IMPLEMENTS,
        "write" => ref_kind::WRITE,
        "bind" => ref_kind::BIND,
        _ => return None,
    })
}

/// When several tags name one node, the most specific wins: a broad `type` or
/// `member` pattern never overrides a call, write, binding or heritage tag.
fn specificity(kind: u8) -> u8 {
    match kind {
        ref_kind::TYPE => 0,
        ref_kind::MEMBER => 1,
        ref_kind::CALL => 2,
        _ => 3,
    }
}

fn text<'s>(src: &'s [u8], n: Node<'_>) -> &'s str {
    std::str::from_utf8(&src[n.byte_range()])
        .unwrap_or("")
        .trim()
}

fn clean_module(s: &str) -> String {
    s.trim_matches(|c: char| matches!(c, '"' | '\'' | '`' | '<' | '>') || c.is_whitespace())
        .split_whitespace()
        .collect()
}

/// Everything one pass of the tag query yields.
#[derive(Default)]
struct Tagged<'t> {
    defs: Vec<Def<'t>>,
    ref_tags: HashMap<usize, u8>,
    imports: Vec<ImportFact>,
    import_nodes: HashSet<usize>,
}

fn run_query<'t>(lang: Lang, root: Node<'t>, src: &[u8]) -> Option<Tagged<'t>> {
    let query = tag_query(lang)?;
    let names = query.capture_names();
    let mut out = Tagged::default();
    let mut seen_imports = HashSet::new();
    let mut cursor = QueryCursor::new();
    let mut matches = cursor.matches(&query, root, src);
    while let Some(m) = matches.next() {
        let (mut def, mut import) = (None, None);
        let (mut name, mut module, mut imported, mut alias) = (None, None, None, None);
        let mut params = Vec::new();
        for c in m.captures {
            let cap = names[c.index as usize];
            match cap.split_once('.') {
                Some(("definition", label)) => def = Some((c.node, label)),
                Some(("import", kind)) => import = Some((c.node, kind)),
                Some(("reference", tag)) => {
                    if let Some(k) = ref_tag(tag) {
                        let e = out.ref_tags.entry(c.node.id()).or_insert(k);
                        if specificity(k) > specificity(*e) {
                            *e = k;
                        }
                    }
                }
                _ => match cap {
                    "name" => name = Some(c.node),
                    "params" => params.push(c.node),
                    "module" => module = Some(c.node),
                    "imported" => imported = Some(c.node),
                    "alias" => alias = Some(c.node),
                    _ => {}
                },
            }
        }
        if let (Some((node, label)), Some(name)) = (def, name)
            && let Some(kind) = fact_kind(label)
        {
            if params.is_empty() {
                let mut c = node.walk();
                params = node
                    .named_children(&mut c)
                    .filter(|n| n.kind() == "parameter")
                    .collect();
            }
            out.defs.push(Def {
                node,
                name,
                params,
                kind,
            });
        }
        if let (Some((node, kind)), Some(module)) = (import, module) {
            let fact = ImportFact {
                line: node.start_position().row as u32 + 1,
                kind: kind.to_string(),
                module: clean_module(text(src, module)),
                name: imported.map(|n| text(src, n).to_string()),
                alias: alias.map(|n| text(src, n).to_string()),
            };
            out.import_nodes.insert(node.id());
            if !fact.module.is_empty() && seen_imports.insert(format!("{fact:?}")) {
                out.imports.push(fact);
            }
        }
    }
    Some(out)
}

/// Extract the facts of one file with its pack's tag query; None without a grammar.
pub fn extract(lang: Lang, text_src: &str) -> Option<FileFacts> {
    let mut parser = Parser::new();
    parser.set_language(&lang.grammar()?).ok()?;
    let tree = parser.parse(text_src, None)?;
    drop(parser);
    let src = text_src.as_bytes();
    let root = tree.root_node();
    let tagged = run_query(lang, root, src)?;
    let mut facts = FileFacts {
        engine: "tree_sitter".into(),
        parse_errors: root.has_error(),
        ..Default::default()
    };
    let def_names = symbols(lang, tagged.defs, src, &mut facts);
    references(
        lang,
        root,
        src,
        &tagged.ref_tags,
        &tagged.import_nodes,
        &def_names,
        &mut facts,
    );
    facts.imports = tagged.imports;
    Some(facts)
}

/// Symbols from the definitions, in source order; returns the declaration name nodes.
fn symbols(
    lang: Lang,
    mut defs: Vec<Def<'_>>,
    src: &[u8],
    facts: &mut FileFacts,
) -> HashSet<usize> {
    defs.sort_by_key(|d| (d.node.start_byte(), std::cmp::Reverse(d.node.end_byte())));
    let mut seen = HashSet::new();
    defs.retain(|d| seen.insert(d.node.id()));
    // every declaration name, kept or past the symbol bound, is never a reference
    let def_names = defs.iter().map(|d| d.name.id()).collect();
    // enclosing symbols: (end byte, symbol index, inside a callable body)
    let mut open: Vec<(usize, u32, bool)> = Vec::new();
    for d in defs {
        let (start, end) = (d.node.start_byte(), d.node.end_byte());
        while open.last().is_some_and(|o| o.0 < end) {
            open.pop();
        }
        if facts.symbols.len() >= MAX_SYMBOLS_PER_FILE {
            facts.symbols_truncated = true;
            break;
        }
        let owner_idx = open.last().map(|o| o.1);
        let owner = owner_idx.map(|i| &facts.symbols[i as usize]);
        let kind = match (d.kind, owner) {
            ("Function", Some(o)) if METHOD_OWNERS.contains(&o.kind.as_str()) => "Method",
            (kind, _) => kind,
        };
        let name = text(src, d.name).to_string();
        let prefix = owner.map(|o| o.qualified_name.clone()).unwrap_or_default();
        let qualified_name = qualify(&prefix, &name, &mut facts.nesting_truncated);
        let local = open.last().is_some_and(|o| o.2);
        let callable = matches!(kind, "Function" | "Method" | "Constructor");
        let header_from = d
            .name
            .start_byte()
            .saturating_sub(MAX_HEADER_BYTES)
            .max(start);
        let header = src
            .get(header_from..d.name.start_byte())
            .and_then(|h| std::str::from_utf8(h).ok())
            .unwrap_or("");
        let pos = d.name.start_position();
        let params: Vec<Option<Node<'_>>> = d.params.iter().copied().map(Some).collect();
        let idx = facts.symbols.len() as u32;
        facts.symbols.push(SymbolFact {
            qualified_name,
            kind: kind.to_string(),
            container: owner_idx,
            arity: callable.then(|| match d.params.as_slice() {
                [list] if list.kind() != "parameter" => list_arity(*list),
                each => each.len() as u32,
            }),
            start_byte: start as u32,
            end_byte: end as u32,
            // a symbol level spans several syntax levels
            start_line: doc_start_line(d.node, open.len() > MAX_DOC_DEPTH / 4)
                .min(pos.row as u32 + 1),
            end_line: decl_end_line(d.node),
            name_line: pos.row as u32 + 1,
            name_col: pos.column as u32,
            signature_hash: signature_hash(src, &params),
            exported: !local && lang.pack().export.exported(&name, header),
            depth: open.len() as u32,
            local,
            uid_suffix: String::new(),
            name,
        });
        open.push((end, idx, local || callable));
    }
    def_names
}

fn references(
    lang: Lang,
    root: Node<'_>,
    src: &[u8],
    ref_tags: &HashMap<usize, u8>,
    import_nodes: &HashSet<usize>,
    def_names: &HashSet<usize>,
    facts: &mut FileFacts,
) {
    let idents = lang.pack().ident_kinds;
    let mut name_ix: HashMap<String, u32> = HashMap::new();
    // symbols enclosing the walk position: (end byte, index); `next` is the first symbol
    // not yet entered (symbols are in start order, like the walk)
    let mut open: Vec<(u32, u32)> = Vec::new();
    let mut next = 0usize;
    // per ancestor: (return path, condition, import) flags it set
    let mut flags: Vec<(bool, bool, bool)> = Vec::new();
    let (mut n_ret, mut n_cond, mut n_import) = (0u32, 0u32, 0u32);
    let mut cursor = root.walk();
    loop {
        let node = cursor.node();
        let f = (
            RETURN_KINDS.contains(&node.kind()),
            cursor.field_name() == Some("condition"),
            import_nodes.contains(&node.id()),
        );
        n_ret += f.0 as u32;
        n_cond += f.1 as u32;
        n_import += f.2 as u32;
        flags.push(f);
        let tag = ref_tags.get(&node.id()).copied();
        let candidate = node.is_named()
            && (tag.is_some() || idents.contains(&node.kind()))
            && !def_names.contains(&node.id());
        if candidate {
            if facts.refs.len() >= MAX_REFS_PER_FILE {
                facts.symbols_truncated = true;
                return;
            }
            let at = node.start_byte() as u32;
            while next < facts.symbols.len() && facts.symbols[next].start_byte <= at {
                let s = &facts.symbols[next];
                while open.last().is_some_and(|o| o.0 <= s.start_byte) {
                    open.pop();
                }
                open.push((s.end_byte, next as u32));
                next += 1;
            }
            while open.last().is_some_and(|o| o.0 <= at) {
                open.pop();
            }
            let name = text(src, node);
            // a name needs a word character (`.` is Bash's source command, `_` a discard)
            if name != "_" && name.chars().any(|c| c.is_alphanumeric() || c == '_') {
                let kind = match (n_import > 0, tag) {
                    (true, _) => ref_kind::IMPORT,
                    (false, Some(k)) => k,
                    (false, None) => ref_kind::READ,
                };
                let fl = flow::classify(n_ret > 0, n_cond > 0, kind == ref_kind::WRITE);
                let ni = *name_ix.entry(name.to_string()).or_insert_with(|| {
                    facts.names.push(name.to_string());
                    (facts.names.len() - 1) as u32
                });
                let pos = node.start_position();
                facts.refs.push(RefFact(
                    ni,
                    pos.row as u32 + 1,
                    pos.column as u32,
                    at,
                    open.last().map(|o| o.1 as i32).unwrap_or(-1),
                    kind,
                    fl,
                    // no qualifier: the scope resolver covers only walker languages
                    -1,
                ));
            }
        }
        if cursor.goto_first_child() {
            continue;
        }
        loop {
            let (r, c, i) = flags.pop().unwrap_or_default();
            n_ret -= r as u32;
            n_cond -= c as u32;
            n_import -= i as u32;
            if cursor.goto_next_sibling() {
                break;
            }
            if !cursor.goto_parent() {
                return;
            }
        }
    }
}
