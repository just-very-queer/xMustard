//! Streaming per-file fact extraction. Each language's pack (`lang::PACKS`) chooses the
//! extractor: the hand-written walker below for Go, Rust, TypeScript/TSX and
//! JavaScript/JSX, the tag-query engine (`lang::tags`) for the other grammar packs, and
//! the precompiled lexical fallback (`lexical`) for files no grammar parses.
//!
//! One file is parsed, walked once with a cursor, and its tree dropped before the next
//! file is read. The walk records:
//! - symbols with their full container path (`Outer.Inner.method`), byte and line
//!   ranges, arity and a signature hash;
//! - references: every identifier occurrence that is not a declaration name. Comments
//!   and string literals contain no identifier nodes, so they never produce references
//!   (the legacy lexical builder took every word of the file, including both);
//! - imports and re-exports;
//! - function-aligned chunks.

use std::collections::{HashMap, HashSet};

use sha2::{Digest, Sha256};
use tree_sitter::{Node, Parser, TreeCursor};

pub use super::lang::Lang;
use super::lang::{Extractor, tags};

use super::chunks;
use super::facts::{FileFacts, ImportFact, RefFact, SymbolFact, flow, ref_kind};
use super::lexical;
use super::uid;

/// Symbols kept per file (same bound as the legacy extractor); more sets
/// `symbols_truncated`.
pub const MAX_SYMBOLS_PER_FILE: usize = crate::treesitter::MAX_SYMBOLS_PER_FILE;
/// References kept per file; a generated file past this is marked truncated. Only
/// lexically extracted files (above the parse bounds) come near it: 200,000 references
/// from a 1 MB single-line file peaked at 26.0 MiB, 100,000 at 22.9 MiB.
pub const MAX_REFS_PER_FILE: usize = 100_000;

/// Bump when extraction output changes for the same bytes. Part of the analyzer version.
pub const EXTRACTOR_REVISION: u32 = 3;

/// Longest container prefix spelled out in a qualified name. A deeper prefix is replaced
/// by `~<hash of the prefix>`, so qualified names, UIDs and scope frames stay bounded
/// whatever the nesting depth (a file of 4,600 nested functions otherwise needs
/// quadratic memory: measured 167 MiB peak for one 64 KB file).
pub const MAX_QUALIFIED_BYTES: usize = 256;

/// Syntax-tree depth past which leading doc comments are not looked up (each lookup
/// walks the tree from the root: quadratic time on a deeply nested file).
pub(crate) const MAX_DOC_DEPTH: usize = 256;

/// Grammar identities for the analyzer version (a grammar bump, or a pack compiled in or
/// out, changes facts).
pub fn grammar_versions() -> String {
    format!(
        "ts-abi-{};{};extractor-{EXTRACTOR_REVISION};lexical-{}",
        tree_sitter::LANGUAGE_VERSION,
        super::lang::grammar_ids(),
        lexical::LEXICAL_REVISION
    )
}

/// Files larger than this are not given to tree-sitter: a parse tree costs about 30
/// bytes per source byte (measured: 4.5 MiB of footprint for one 95 KB TypeScript file),
/// so the largest parsed file sets the worker's peak. At 64 KiB the cline fixture builds
/// at 22.7-24.9 MiB peak RSS; at 128 KiB it measured 24.5-25.8 MiB. Larger files get the
/// lexical extractor instead, reported as the `lexical_fallback` coverage loss.
pub const DEFAULT_MAX_PARSE_BYTES: usize = 64 << 10;

/// Nesting past which a grammar file is extracted lexically: brackets, `<` left open
/// in one statement (generic arguments, or comparisons that tree-sitter keeps
/// ambiguous), and runs of prefix-operator characters (`- ! ~ & * ^ + | <`). Tree-sitter's
/// parse stack and tree grow with nesting depth (measured on 64 KB files: 4,600 nested
/// functions 24 MiB, 64,000 `-` 53.9 MiB, 32,000 `a<` 68.1 MiB), while real code stays far
/// below this; deeper files are reported as `lexical_fallback` and `nesting_truncated`.
pub const MAX_PARSE_NESTING: usize = 256;

/// Tokens (an upper bound: operators count per character, literal and comment words
/// count too) past which a grammar file is extracted lexically, reported as
/// `lexical_fallback`. Tree memory grows with tokens whatever the shape: 32,000 `a;`
/// statements in 64 KB peaked at 26.2 MiB. Among 11,687 real Go, Rust, TS and JS files up
/// to 64 KB (cline, pi-mono, gitnexus, OpenHands, letta-code and this repository) the
/// densest has 19,508; the most expensive shapes the nesting guards admit peak at
/// 23-24 MiB at this bound in an otherwise empty repository.
pub const MAX_PARSE_TOKENS: usize = 24_000;

/// Why tree-sitter does not get a file, if it does not.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ParseLimit {
    NoGrammar,
    /// Above `max_parse_bytes`.
    Size,
    Tokens,
    Nesting,
}

/// Why `lang` text of these bytes is extracted lexically; None when tree-sitter parses it.
fn parse_refusal(lang: Lang, bytes: &[u8], max_parse_bytes: usize) -> Option<ParseLimit> {
    match (lang.has_grammar(), bytes.len() <= max_parse_bytes) {
        (false, _) => Some(ParseLimit::NoGrammar),
        (true, false) => Some(ParseLimit::Size),
        (true, true) => parse_limit(bytes),
    }
}

/// One pass over `bytes` for the parse guards (`MAX_PARSE_TOKENS`, `MAX_PARSE_NESTING`);
/// stops at the first bound crossed.
fn parse_limit(bytes: &[u8]) -> Option<ParseLimit> {
    let word = |c: u8| c.is_ascii_alphanumeric() || c == b'_' || c == b'$';
    let (mut tokens, mut depth, mut run) = (0usize, 0usize, 0usize);
    // `<` left open per bracket level (the stack is as deep as the brackets, so bounded)
    let mut angles: Vec<usize> = vec![0];
    let n = bytes.len();
    let mut i = 0;
    while i < n {
        let c = bytes[i];
        if c.is_ascii_whitespace() {
            i += 1;
            continue;
        }
        tokens += 1;
        if tokens > MAX_PARSE_TOKENS {
            return Some(ParseLimit::Tokens);
        }
        if word(c) {
            while i < n && word(bytes[i]) {
                i += 1;
            }
            run = 0;
            continue;
        }
        if c >= 0x80 {
            // one token per character
            i += 1;
            while i < n && (0x80..0xc0).contains(&bytes[i]) {
                i += 1;
            }
            run = 0;
            continue;
        }
        let prev = if i > 0 { bytes[i - 1] } else { 0 };
        let next = bytes.get(i + 1).copied().unwrap_or(0);
        let open = angles.last_mut().expect("the root level is never popped");
        match c {
            b'{' | b'(' | b'[' => {
                depth += 1;
                if depth > MAX_PARSE_NESTING {
                    return Some(ParseLimit::Nesting);
                }
                angles.push(0);
                run = 0;
            }
            b'}' | b')' | b']' => {
                depth = depth.saturating_sub(1);
                if angles.len() > 1 {
                    angles.pop();
                }
                if c == b'}' {
                    *angles.last_mut().expect("root level") = 0;
                }
                run = 0;
            }
            b';' => {
                *open = 0;
                run = 0;
            }
            b'>' => {
                if !matches!(prev, b'=' | b'-') && next != b'=' {
                    *open = open.saturating_sub(1);
                }
                run = 0;
            }
            b'<' | b'-' | b'+' | b'!' | b'~' | b'&' | b'*' | b'^' | b'|' => {
                if c == b'<' && !matches!(next, b'=' | b'<' | b'-') && prev != b'<' {
                    *open += 1;
                    if *open > MAX_PARSE_NESTING {
                        return Some(ParseLimit::Nesting);
                    }
                }
                run += 1;
                if run > MAX_PARSE_NESTING {
                    return Some(ParseLimit::Nesting);
                }
            }
            _ => run = 0,
        }
        i += 1;
    }
    None
}

/// Extract the facts of one file. `text` is the decoded source (lossy when
/// `invalid_utf8`). Grammar files within `max_parse_bytes`, `MAX_PARSE_TOKENS` and
/// `MAX_PARSE_NESTING` are parsed with tree-sitter; the rest use the lexical extractor.
pub fn extract(lang: Lang, text: &str, invalid_utf8: bool, max_parse_bytes: usize) -> FileFacts {
    let refusal = parse_refusal(lang, text.as_bytes(), max_parse_bytes);
    let mut facts = match refusal {
        None => extract_tree_sitter(lang, text).unwrap_or_else(|| lexical::extract(lang, text)),
        Some(_) => lexical::extract(lang, text),
    };
    facts.lexical_fallback = lang.has_grammar() && facts.engine != "tree_sitter";
    facts.nesting_truncated |= refusal == Some(ParseLimit::Nesting);
    facts.invalid_utf8 = invalid_utf8;
    uid::assign_suffixes(&mut facts.symbols);
    let starts = chunks::line_starts(text.as_bytes());
    facts.line_count = chunks::line_count(&starts);
    facts.chunks = chunks::plan_chunks(&facts.symbols, text.as_bytes(), &starts);
    facts
}

/// Which extractor `extract` uses for this (decoded) text: part of the fact-cache key,
/// so a change of `max_parse_bytes` never serves facts made the other way.
pub fn extraction_mode(lang: Lang, text: &[u8], max_parse_bytes: usize) -> &'static str {
    match parse_refusal(lang, text, max_parse_bytes) {
        None => "ts",
        Some(_) => "lex",
    }
}

fn extract_tree_sitter(lang: Lang, text: &str) -> Option<FileFacts> {
    match lang.pack().extractor {
        Extractor::Walker => walk_file(lang, text),
        Extractor::Tags => tags::extract(lang, text),
    }
}

fn walk_file(lang: Lang, text: &str) -> Option<FileFacts> {
    // A fresh parser per file: its internal stack is freed with it, and only grammars
    // of files actually parsed are ever touched.
    let mut parser = Parser::new();
    parser.set_language(&lang.grammar()?).ok()?;
    let tree = parser.parse(text, None)?;
    drop(parser);
    let root = tree.root_node();
    let mut w = Walker::new(lang, text.as_bytes());
    w.parse_errors = root.has_error();
    w.walk(root.walk());
    let facts = w.finish();
    drop(tree);
    Some(facts)
}

/// What a scope frame contributes to the names and containers of nested nodes. Its
/// qualified prefix is its symbol's qualified name (never copied into the frame, so a
/// deep nesting costs one bounded name per symbol, not per frame).
struct Scope {
    node_id: usize,
    symbol: Option<u32>,
    callable: bool,
    /// Symbol kind that owns the scope (Impl/Trait/Class...), for method detection.
    kind: &'static str,
}

/// Ancestor entry: which walk counters this node incremented (undone on leave).
#[derive(Default, Clone, Copy)]
struct Counters {
    ret: bool,
    cond: bool,
    import: bool,
    extends: bool,
    implements: bool,
    bind: bool,
    skip: bool,
}

struct Anc {
    id: usize,
    kind: &'static str,
    field: Option<&'static str>,
    counters: Counters,
}

/// A name bound to a function- or class-valued expression by its declarator.
#[derive(Clone)]
struct PendingName {
    name: String,
    /// Symbol kind to create for the value (Function/Method/Class).
    kind: &'static str,
    exported: bool,
    /// First line of the declaration including its doc comments.
    doc_line: u32,
}

struct Walker<'a> {
    lang: Lang,
    src: &'a [u8],
    parse_errors: bool,
    symbols: Vec<SymbolFact>,
    refs: Vec<RefFact>,
    names: Vec<String>,
    name_ix: HashMap<&'a str, u32>,
    imports: Vec<ImportFact>,
    truncated: bool,
    refs_truncated: bool,
    /// Some qualified name hit MAX_QUALIFIED_BYTES.
    nesting_truncated: bool,
    anc: Vec<Anc>,
    scopes: Vec<Scope>,
    def_names: HashSet<usize>,
    /// value node id → name for `const f = () => ...`, class fields with function
    /// values, and Go `f := func() {}`.
    pending: HashMap<usize, PendingName>,
    /// object-literal node id → owning symbol (its declarator), whose qualified name is
    /// the prefix of the object's members.
    object_scopes: HashMap<usize, u32>,
    /// Go method symbol index → receiver type name, resolved after the walk.
    go_receivers: Vec<(u32, String)>,
    /// Names listed in local `export { ... }` clauses (JS/TS).
    exported_names: HashSet<String>,
    n_ret: u32,
    n_cond: u32,
    n_import: u32,
    n_extends: u32,
    n_implements: u32,
    n_bind: u32,
    n_skip: u32,
}

fn is_function_value(kind: &str) -> bool {
    matches!(
        kind,
        "arrow_function"
            | "function_expression"
            | "function"
            | "generator_function"
            | "closure_expression"
            | "func_literal"
    )
}

fn normalize_ws(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// `prefix.name`, with a prefix longer than MAX_QUALIFIED_BYTES replaced by
/// `~<16 hex of its hash>` (deterministic, so UIDs stay stable); sets `truncated` then.
pub(crate) fn qualify(prefix: &str, name: &str, truncated: &mut bool) -> String {
    if prefix.is_empty() {
        return name.to_string();
    }
    if prefix.len() + 1 + name.len() <= MAX_QUALIFIED_BYTES {
        return format!("{prefix}.{name}");
    }
    *truncated = true;
    let h = format!("{:x}", Sha256::digest(prefix.as_bytes()));
    format!("~{}.{name}", &h[..16])
}

/// Hash of the whitespace-normalized text of `parts`; empty when there are none.
pub(crate) fn signature_hash(src: &[u8], parts: &[Option<Node<'_>>]) -> String {
    let mut s = String::new();
    for p in parts.iter().flatten() {
        s.push_str(&normalize_ws(
            std::str::from_utf8(&src[p.byte_range()]).unwrap_or(""),
        ));
        s.push('|');
    }
    if s.is_empty() {
        return String::new();
    }
    format!("{:x}", Sha256::digest(s.as_bytes()))[..16].to_string()
}

/// Last line of a declaration node (one ending at column 0 ends on the previous line).
pub(crate) fn decl_end_line(decl: Node<'_>) -> u32 {
    let end = decl.end_position();
    if end.column == 0 && end.row > decl.start_position().row {
        end.row as u32
    } else {
        end.row as u32 + 1
    }
}

/// Leading doc comments and attributes directly above `outer`: the first line of the
/// contiguous run, or `outer`'s own line. `too_deep` skips the lookup: tree-sitter's
/// sibling lookups walk down from the root, so they cost O(depth) each, and past
/// MAX_DOC_DEPTH a declaration starts at its own line.
pub(crate) fn doc_start_line(outer: Node<'_>, too_deep: bool) -> u32 {
    let mut line = outer.start_position().row as u32 + 1;
    if too_deep {
        return line;
    }
    let mut prev = outer.prev_sibling();
    while let Some(p) = prev {
        let is_doc = matches!(
            p.kind(),
            "comment"
                | "line_comment"
                | "block_comment"
                | "attribute_item"
                | "decorator"
                | "attribute_list"
        );
        let end = p.end_position().row as u32 + 1;
        // a line comment's node may end at column 0 of the next line.
        let end = if p.end_position().column == 0 && end > 1 {
            end - 1
        } else {
            end
        };
        if !is_doc || end + 1 < line {
            break;
        }
        line = p.start_position().row as u32 + 1;
        prev = p.prev_sibling();
    }
    line
}

impl<'a> Walker<'a> {
    fn new(lang: Lang, src: &'a [u8]) -> Self {
        Walker {
            lang,
            src,
            parse_errors: false,
            symbols: Vec::new(),
            refs: Vec::new(),
            names: Vec::new(),
            name_ix: HashMap::new(),
            imports: Vec::new(),
            truncated: false,
            refs_truncated: false,
            nesting_truncated: false,
            anc: Vec::with_capacity(64),
            scopes: Vec::with_capacity(16),
            def_names: HashSet::new(),
            pending: HashMap::new(),
            object_scopes: HashMap::new(),
            go_receivers: Vec::new(),
            exported_names: HashSet::new(),
            n_ret: 0,
            n_cond: 0,
            n_import: 0,
            n_extends: 0,
            n_implements: 0,
            n_bind: 0,
            n_skip: 0,
        }
    }

    fn text(&self, n: Node) -> &'a str {
        std::str::from_utf8(&self.src[n.byte_range()]).unwrap_or("")
    }

    fn walk(&mut self, mut cursor: TreeCursor<'_>) {
        loop {
            let node = cursor.node();
            let field = cursor.field_name();
            self.enter(node, field);
            if cursor.goto_first_child() {
                continue;
            }
            loop {
                self.leave(cursor.node());
                if cursor.goto_next_sibling() {
                    break;
                }
                if !cursor.goto_parent() {
                    return;
                }
            }
        }
    }

    fn parent(&self) -> Option<&Anc> {
        // the current node is on top of `anc` once pushed; its parent is below it.
        let n = self.anc.len();
        (n >= 2).then(|| &self.anc[n - 2])
    }

    fn grandparent(&self) -> Option<&Anc> {
        let n = self.anc.len();
        (n >= 3).then(|| &self.anc[n - 3])
    }

    fn in_callable(&self) -> bool {
        self.scopes.iter().any(|s| s.callable)
    }

    fn container_symbol(&self) -> Option<u32> {
        self.scopes.iter().rev().find_map(|s| s.symbol)
    }

    fn owner_kind(&self) -> &'static str {
        self.scopes
            .iter()
            .rev()
            .find(|s| s.symbol.is_some())
            .map(|s| s.kind)
            .unwrap_or("")
    }

    fn qual_prefix(&self) -> &str {
        self.scopes
            .last()
            .and_then(|s| s.symbol)
            .and_then(|i| self.symbols.get(i as usize))
            .map(|s| s.qualified_name.as_str())
            .unwrap_or("")
    }

    fn enter(&mut self, node: Node<'_>, field: Option<&'static str>) {
        let kind = node.kind();
        let parent_kind = self.anc.last().map(|a| a.kind).unwrap_or("");
        let mut c = Counters::default();
        match kind {
            "return_statement" | "return_expression" => c.ret = true,
            "import_statement"
            | "import_declaration"
            | "use_declaration"
            | "extern_crate_declaration" => c.import = true,
            "export_statement" if node.child_by_field_name("source").is_some() => c.import = true,
            "class_heritage" if self.lang == Lang::JavaScript => c.extends = true,
            "extends_clause" | "extends_type_clause" => c.extends = true,
            "implements_clause" => c.implements = true,
            "package_clause"
            | "lifetime"
            | "label_name"
            | "statement_identifier"
            | "metavariable" => c.skip = true,
            _ => {}
        }
        if field == Some("condition")
            || (field == Some("value")
                && matches!(
                    parent_kind,
                    "match_expression" | "switch_statement" | "expression_switch_statement"
                ))
        {
            c.cond = true;
        }
        if field == Some("trait") && parent_kind == "impl_item" {
            c.implements = true;
        }
        if field == Some("bounds") && parent_kind == "trait_item" {
            c.extends = true;
        }
        if self.binding_position(node, field, parent_kind) {
            c.bind = true;
        }
        self.bump(c, 1);
        self.anc.push(Anc {
            id: node.id(),
            kind,
            field,
            counters: c,
        });

        if node.is_named() {
            self.declare(node, kind, field);
            self.maybe_import(node, kind);
            self.maybe_ref(node, kind, field);
        }
    }

    fn leave(&mut self, node: Node<'_>) {
        if let Some(a) = self.anc.pop() {
            debug_assert_eq!(a.id, node.id());
            let c = a.counters;
            self.bump(c, -1);
        }
        while self.scopes.last().is_some_and(|s| s.node_id == node.id()) {
            self.scopes.pop();
        }
    }

    fn bump(&mut self, c: Counters, d: i32) {
        let apply = |v: &mut u32, on: bool| {
            if on {
                *v = (*v as i32 + d).max(0) as u32;
            }
        };
        apply(&mut self.n_ret, c.ret);
        apply(&mut self.n_cond, c.cond);
        apply(&mut self.n_import, c.import);
        apply(&mut self.n_extends, c.extends);
        apply(&mut self.n_implements, c.implements);
        apply(&mut self.n_bind, c.bind);
        apply(&mut self.n_skip, c.skip);
    }

    /// Whether `node` (reached through `field` of a `parent_kind` node) is a pattern that
    /// declares local names.
    fn binding_position(&self, node: Node<'_>, field: Option<&str>, parent_kind: &str) -> bool {
        let f = field.unwrap_or("");
        match self.lang {
            Lang::Rust => {
                matches!(
                    (parent_kind, f),
                    ("let_declaration", "pattern")
                        | ("parameter", "pattern")
                        | ("for_expression", "pattern")
                        | ("let_condition", "pattern")
                        | ("match_pattern", _)
                ) || parent_kind == "closure_parameters"
            }
            Lang::Go => {
                matches!(
                    (parent_kind, f),
                    ("parameter_declaration", "name")
                        | ("variadic_parameter_declaration", "name")
                        | ("range_clause", "left")
                ) || (parent_kind == "short_var_declaration" && f == "left")
                    || (matches!(parent_kind, "var_spec" | "const_spec")
                        && f == "name"
                        && self.in_callable())
            }
            _ => {
                matches!(
                    (parent_kind, f),
                    ("required_parameter", "pattern")
                        | ("optional_parameter", "pattern")
                        | ("catch_clause", "parameter")
                        | ("arrow_function", "parameter")
                        | ("for_in_statement", "left")
                ) || (parent_kind == "formal_parameters"
                    && matches!(
                        node.kind(),
                        "identifier" | "object_pattern" | "array_pattern" | "rest_pattern"
                            | "assignment_pattern"
                    ))
                    // a declarator's name binds a local; names that declare symbols are
                    // recorded as definitions and never reach `maybe_ref`.
                    || (parent_kind == "variable_declarator" && f == "name")
            }
        }
    }

    fn intern(&mut self, name: &'a str) -> u32 {
        if let Some(&i) = self.name_ix.get(name) {
            return i;
        }
        let i = self.names.len() as u32;
        self.names.push(name.to_string());
        self.name_ix.insert(name, i);
        i
    }

    fn maybe_ref(&mut self, node: Node<'_>, kind: &'static str, field: Option<&'static str>) {
        let is_ident = match self.lang {
            Lang::Rust => matches!(
                kind,
                "identifier"
                    | "type_identifier"
                    | "field_identifier"
                    | "shorthand_field_identifier"
            ),
            Lang::Go => matches!(
                kind,
                "identifier" | "type_identifier" | "field_identifier" | "package_identifier"
            ),
            _ => matches!(
                kind,
                "identifier"
                    | "type_identifier"
                    | "property_identifier"
                    | "shorthand_property_identifier"
                    | "shorthand_property_identifier_pattern"
                    | "private_property_identifier"
            ),
        };
        if !is_ident || self.n_skip > 0 || self.def_names.contains(&node.id()) {
            return;
        }
        let name = self.text(node).trim();
        if name.is_empty() || name == "_" {
            return;
        }
        if self.refs.len() >= MAX_REFS_PER_FILE {
            self.refs_truncated = true;
            return;
        }
        let rk = self.ref_kind(kind, field);
        let fl = if self.n_ret > 0 {
            flow::RETURNS
        } else if self.n_cond > 0 {
            flow::BRANCHES
        } else if rk == ref_kind::WRITE {
            flow::WRITES
        } else {
            flow::NONE
        };
        let pos = node.start_position();
        let container = self.container_symbol().map(|s| s as i32).unwrap_or(-1);
        let ni = self.intern(name);
        if rk == ref_kind::EXPORT {
            self.exported_names.insert(name.to_string());
        }
        self.refs.push(RefFact(
            ni,
            pos.row as u32 + 1,
            pos.column as u32,
            node.start_byte() as u32,
            container,
            rk,
            fl,
        ));
    }

    fn ref_kind(&self, kind: &str, field: Option<&str>) -> u8 {
        if self.n_import > 0 {
            return ref_kind::IMPORT;
        }
        if self.n_extends > 0 {
            return ref_kind::EXTENDS;
        }
        if self.n_implements > 0 {
            return ref_kind::IMPLEMENTS;
        }
        if self.n_bind > 0 {
            return ref_kind::BIND;
        }
        let parent = self.parent();
        let pk = parent.map(|p| p.kind).unwrap_or("");
        let pf = parent.and_then(|p| p.field).unwrap_or("");
        let gk = self.grandparent().map(|g| g.kind).unwrap_or("");
        let f = field.unwrap_or("");
        let is_assign = |k: &str| {
            matches!(
                k,
                "assignment_expression"
                    | "augmented_assignment_expression"
                    | "compound_assignment_expr"
                    | "assignment_statement"
            )
        };
        if (pk == "call_expression" && f == "function")
            || (pk == "new_expression" && f == "constructor")
            || (pk == "macro_invocation" && f == "macro")
        {
            return ref_kind::CALL;
        }
        let member = (pk == "member_expression" && f == "property")
            || (pk == "field_expression" && f == "field")
            || (pk == "selector_expression" && f == "field")
            || (pk == "scoped_identifier" && f == "name");
        if member {
            if pf == "function" && gk == "call_expression" {
                return ref_kind::MEMBER_CALL;
            }
            if pf == "left" && is_assign(gk) {
                return ref_kind::WRITE;
            }
            return ref_kind::MEMBER;
        }
        if pk == "scoped_type_identifier" && f == "name" {
            return ref_kind::TYPE;
        }
        if (is_assign(pk) && f == "left")
            || pk == "update_expression"
            || pk == "inc_statement"
            || pk == "dec_statement"
            || (pk == "expression_list" && pf == "left" && gk == "assignment_statement")
        {
            return ref_kind::WRITE;
        }
        if pk == "export_specifier" {
            return ref_kind::EXPORT;
        }
        if kind == "type_identifier" {
            return ref_kind::TYPE;
        }
        ref_kind::READ
    }

    /// Leading doc comments and attributes directly above `outer`: the first line of the
    /// contiguous run, or `outer`'s own line.
    fn doc_start_line(&self, outer: Node<'_>) -> u32 {
        doc_start_line(outer, self.anc.len() > MAX_DOC_DEPTH)
    }

    fn arity_of(&self, params: Option<Node<'_>>) -> u32 {
        let Some(p) = params else { return 0 };
        let mut c = p.walk();
        match p.kind() {
            // an arrow function's bare `x => ...` parameter
            "identifier" => 1,
            "parameters" => p
                .named_children(&mut c)
                .filter(|n| matches!(n.kind(), "parameter" | "variadic_parameter"))
                .count() as u32,
            "parameter_list" => p
                .named_children(&mut c)
                .map(|n| match n.kind() {
                    "parameter_declaration" => {
                        let mut c2 = n.walk();
                        let names = n.children_by_field_name("name", &mut c2).count() as u32;
                        names.max(1)
                    }
                    "variadic_parameter_declaration" => 1,
                    _ => 0,
                })
                .sum(),
            "closure_parameters" => p
                .named_children(&mut c)
                .filter(|n| !matches!(n.kind(), "line_comment" | "block_comment"))
                .count() as u32,
            _ => p
                .named_children(&mut c)
                .filter(|n| {
                    // TypeScript's `this: T` is a type annotation, not a parameter.
                    let this_param = n.kind() == "required_parameter"
                        && n.child_by_field_name("pattern")
                            .is_some_and(|pat| pat.kind() == "this");
                    n.kind() != "comment" && !this_param
                })
                .count() as u32,
        }
    }

    fn signature_hash(&self, parts: &[Option<Node<'_>>]) -> String {
        signature_hash(self.src, parts)
    }

    #[allow(clippy::too_many_arguments)]
    fn add_symbol(
        &mut self,
        decl: Node<'_>,
        start_line: u32,
        name_node: Option<Node<'_>>,
        name: String,
        kind: &'static str,
        arity: Option<u32>,
        sig: String,
        exported: bool,
        push_scope: bool,
        qual_override: Option<String>,
    ) -> Option<u32> {
        if let Some(n) = name_node {
            self.def_names.insert(n.id());
        }
        if name.is_empty() {
            return None;
        }
        if self.symbols.len() >= MAX_SYMBOLS_PER_FILE {
            self.truncated = true;
            return None;
        }
        let qualified = match qual_override {
            Some(q) => q,
            None => {
                let prefix = self.qual_prefix().to_string();
                self.qualify(&prefix, &name)
            }
        };
        let at = name_node.unwrap_or(decl);
        let pos = at.start_position();
        let idx = self.symbols.len() as u32;
        let callable = matches!(kind, "Function" | "Method" | "Constructor");
        let depth = self.scopes.iter().filter(|s| s.symbol.is_some()).count() as u32;
        let end_line = decl_end_line(decl);
        self.symbols.push(SymbolFact {
            name,
            qualified_name: qualified,
            kind: kind.to_string(),
            container: self.container_symbol(),
            arity: if callable {
                Some(arity.unwrap_or(0))
            } else {
                None
            },
            start_byte: decl.start_byte() as u32,
            end_byte: decl.end_byte() as u32,
            start_line: start_line.min(pos.row as u32 + 1),
            end_line,
            name_line: pos.row as u32 + 1,
            name_col: pos.column as u32,
            signature_hash: sig,
            exported,
            depth,
            local: self.in_callable(),
            uid_suffix: String::new(),
        });
        if push_scope {
            self.scopes.push(Scope {
                node_id: decl.id(),
                symbol: Some(idx),
                callable,
                kind,
            });
        }
        Some(idx)
    }

    /// `prefix.name`, with a prefix longer than MAX_QUALIFIED_BYTES replaced by
    /// `~<16 hex of its hash>` (deterministic, so UIDs stay stable).
    fn qualify(&mut self, prefix: &str, name: &str) -> String {
        qualify(prefix, name, &mut self.nesting_truncated)
    }

    fn name_of<'t>(&self, node: Node<'t>, field: &str) -> Option<(Node<'t>, String)> {
        let n = node.child_by_field_name(field)?;
        let t = self.text(n).trim().to_string();
        (!t.is_empty()).then_some((n, t))
    }
}

fn has_child_kind(node: Node<'_>, kind: &str) -> bool {
    let mut c = node.walk();
    node.children(&mut c).any(|n| n.kind() == kind)
}

fn strip_quotes(s: &str) -> String {
    s.trim()
        .trim_matches(|c| c == '"' || c == '\'' || c == '`')
        .to_string()
}

fn go_exported(name: &str) -> bool {
    name.chars().next().is_some_and(|c| c.is_uppercase())
}

/// The type name an impl block or Go receiver is about: `Foo` for `Foo`, `Foo<T>`,
/// `&mut Foo`, `*Foo`, `a::b::Foo`.
fn base_type_name<'t>(n: Node<'t>, src: &[u8]) -> String {
    let text = |n: Node<'t>| {
        std::str::from_utf8(&src[n.byte_range()])
            .unwrap_or("")
            .trim()
            .to_string()
    };
    match n.kind() {
        "type_identifier" | "identifier" => text(n),
        "generic_type" => n
            .child_by_field_name("type")
            .map(|t| base_type_name(t, src))
            .unwrap_or_else(|| text(n)),
        "scoped_type_identifier" | "qualified_type" => n
            .child_by_field_name("name")
            .map(|t| base_type_name(t, src))
            .unwrap_or_else(|| text(n)),
        "pointer_type" | "reference_type" => {
            let mut c = n.walk();
            let inner = n
                .named_children(&mut c)
                .find(|ch| ch.kind() != "mutable_specifier" && ch.kind() != "lifetime");
            inner
                .map(|t| base_type_name(t, src))
                .unwrap_or_else(|| text(n))
        }
        _ => normalize_ws(&text(n)),
    }
}

impl<'a> Walker<'a> {
    fn declare(&mut self, node: Node<'_>, kind: &'static str, field: Option<&'static str>) {
        if let Some(p) = self.pending.remove(&node.id()) {
            let params = node
                .child_by_field_name("parameters")
                .or_else(|| node.child_by_field_name("parameter"));
            let callable = p.kind != "Class";
            let sig = if callable {
                self.signature_hash(&[
                    params,
                    node.child_by_field_name("return_type"),
                    node.child_by_field_name("result"),
                ])
            } else {
                String::new()
            };
            let arity = callable.then(|| self.arity_of(params));
            self.add_symbol(
                node, p.doc_line, None, p.name, p.kind, arity, sig, p.exported, true, None,
            );
            return;
        }
        if let Some(sym) = self.object_scopes.remove(&node.id()) {
            self.scopes.push(Scope {
                node_id: node.id(),
                symbol: Some(sym),
                callable: false,
                kind: "Const",
            });
            return;
        }
        match self.lang {
            Lang::Rust => self.declare_rust(node, kind, field),
            Lang::Go => self.declare_go(node, kind, field),
            _ => self.declare_js(node, kind, field),
        }
    }

    fn declare_rust(&mut self, node: Node<'_>, kind: &'static str, _field: Option<&'static str>) {
        let exported = has_child_kind(node, "visibility_modifier");
        let doc = self.doc_start_line(node);
        let simple = |k: &'static str| -> Option<&'static str> { Some(k) };
        let (sym_kind, scope) = match kind {
            "function_item" | "function_signature_item" => {
                let k = if matches!(self.owner_kind(), "Impl" | "Trait") {
                    "Method"
                } else {
                    "Function"
                };
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let params = node.child_by_field_name("parameters");
                let sig = self.signature_hash(&[params, node.child_by_field_name("return_type")]);
                let arity = self.arity_of(params);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    k,
                    Some(arity),
                    sig,
                    exported,
                    kind == "function_item",
                    None,
                );
                return;
            }
            "struct_item" => (simple("Struct"), true),
            "enum_item" => (simple("Enum"), true),
            "union_item" => (simple("Union"), true),
            "trait_item" => (simple("Trait"), true),
            "type_item" => (simple("TypeAlias"), false),
            "const_item" => (simple("Const"), false),
            "static_item" => (simple("Variable"), false),
            "macro_definition" => (simple("Macro"), false),
            "mod_item" => {
                let has_body = node.child_by_field_name("body").is_some();
                if !has_body && let Some((_, name)) = self.name_of(node, "name") {
                    self.imports.push(ImportFact {
                        line: node.start_position().row as u32 + 1,
                        kind: "mod".into(),
                        module: name,
                        name: None,
                        alias: None,
                    });
                }
                (simple("Module"), has_body)
            }
            "impl_item" => {
                let Some(ty) = node.child_by_field_name("type") else {
                    return;
                };
                let name = base_type_name(ty, self.src);
                let sig = self.signature_hash(&[node.child_by_field_name("trait"), Some(ty)]);
                self.add_symbol(node, doc, None, name, "Impl", None, sig, false, true, None);
                return;
            }
            "field_declaration" => {
                if self.parent().map(|p| p.kind) != Some("field_declaration_list") {
                    return;
                }
                (simple("Property"), false)
            }
            "enum_variant" => (simple("EnumMember"), false),
            "let_declaration" => {
                if let (Some(pat), Some(val)) = (
                    node.child_by_field_name("pattern"),
                    node.child_by_field_name("value"),
                ) && pat.kind() == "identifier"
                    && val.kind() == "closure_expression"
                {
                    self.def_names.insert(pat.id());
                    self.pending.insert(
                        val.id(),
                        PendingName {
                            name: self.text(pat).to_string(),
                            kind: "Function",
                            exported: false,
                            doc_line: doc,
                        },
                    );
                }
                return;
            }
            _ => return,
        };
        let Some(sym_kind) = sym_kind else { return };
        let Some((name_n, name)) = self.name_of(node, "name") else {
            return;
        };
        self.add_symbol(
            node,
            doc,
            Some(name_n),
            name,
            sym_kind,
            None,
            String::new(),
            exported,
            scope,
            None,
        );
    }

    fn declare_go(&mut self, node: Node<'_>, kind: &'static str, _field: Option<&'static str>) {
        let doc = self.doc_start_line(node);
        match kind {
            "function_declaration" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let params = node.child_by_field_name("parameters");
                let sig = self.signature_hash(&[params, node.child_by_field_name("result")]);
                let arity = self.arity_of(params);
                let exported = go_exported(&name);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "Function",
                    Some(arity),
                    sig,
                    exported,
                    true,
                    None,
                );
            }
            "method_declaration" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let recv = node
                    .child_by_field_name("receiver")
                    .and_then(|r| {
                        let mut c = r.walk();
                        r.named_children(&mut c)
                            .find(|p| p.kind() == "parameter_declaration")
                    })
                    .and_then(|p| p.child_by_field_name("type"))
                    .map(|t| base_type_name(t, self.src))
                    .unwrap_or_default();
                let params = node.child_by_field_name("parameters");
                let sig = self.signature_hash(&[params, node.child_by_field_name("result")]);
                let arity = self.arity_of(params);
                let exported = go_exported(&name);
                let qual = self.qualify(&recv, &name);
                if let Some(idx) = self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "Method",
                    Some(arity),
                    sig,
                    exported,
                    true,
                    Some(qual),
                ) && !recv.is_empty()
                {
                    self.go_receivers.push((idx, recv));
                }
            }
            "type_spec" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let k = match node.child_by_field_name("type").map(|t| t.kind()) {
                    Some("struct_type") => "Struct",
                    Some("interface_type") => "Interface",
                    _ => "TypeAlias",
                };
                let exported = go_exported(&name);
                // doc comments sit above the enclosing `type` declaration.
                let doc = self.doc_start_line(self_parent_or(node));
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    k,
                    None,
                    String::new(),
                    exported,
                    true,
                    None,
                );
            }
            "type_alias" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let exported = go_exported(&name);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "TypeAlias",
                    None,
                    String::new(),
                    exported,
                    false,
                    None,
                );
            }
            "field_declaration" => {
                if self.parent().map(|p| p.kind) != Some("field_declaration_list") {
                    return;
                }
                let mut c = node.walk();
                let names: Vec<Node<'_>> = node.children_by_field_name("name", &mut c).collect();
                for n in names {
                    let name = self.text(n).trim().to_string();
                    let exported = go_exported(&name);
                    self.add_symbol(
                        node,
                        doc,
                        Some(n),
                        name,
                        "Property",
                        None,
                        String::new(),
                        exported,
                        false,
                        None,
                    );
                }
            }
            "method_elem" | "method_spec" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let params = node.child_by_field_name("parameters");
                let sig = self.signature_hash(&[params, node.child_by_field_name("result")]);
                let arity = self.arity_of(params);
                let exported = go_exported(&name);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "Method",
                    Some(arity),
                    sig,
                    exported,
                    false,
                    None,
                );
            }
            "const_spec" | "var_spec" => {
                let mut c = node.walk();
                let names: Vec<Node<'_>> = node.children_by_field_name("name", &mut c).collect();
                let value = node.child_by_field_name("value");
                let single_func = value.and_then(|v| {
                    let mut c2 = v.walk();
                    let items: Vec<Node<'_>> = v.named_children(&mut c2).collect();
                    (items.len() == 1 && items[0].kind() == "func_literal").then(|| items[0])
                });
                if names.len() == 1
                    && let Some(f) = single_func
                {
                    let name = self.text(names[0]).trim().to_string();
                    self.def_names.insert(names[0].id());
                    let exported = go_exported(&name) && !self.in_callable();
                    self.pending.insert(
                        f.id(),
                        PendingName {
                            name,
                            kind: "Function",
                            exported,
                            doc_line: doc,
                        },
                    );
                    return;
                }
                if self.in_callable() {
                    return; // locals are binding references
                }
                let k = if kind == "const_spec" {
                    "Const"
                } else {
                    "Variable"
                };
                let doc = self.doc_start_line(self_parent_or(node));
                for n in names {
                    let name = self.text(n).trim().to_string();
                    let exported = go_exported(&name);
                    self.add_symbol(
                        node,
                        doc,
                        Some(n),
                        name,
                        k,
                        None,
                        String::new(),
                        exported,
                        false,
                        None,
                    );
                }
            }
            "short_var_declaration" => {
                let (Some(left), Some(right)) = (
                    node.child_by_field_name("left"),
                    node.child_by_field_name("right"),
                ) else {
                    return;
                };
                let mut c1 = left.walk();
                let lhs: Vec<Node<'_>> = left.named_children(&mut c1).collect();
                let mut c2 = right.walk();
                let rhs: Vec<Node<'_>> = right.named_children(&mut c2).collect();
                if lhs.len() == 1
                    && rhs.len() == 1
                    && lhs[0].kind() == "identifier"
                    && rhs[0].kind() == "func_literal"
                {
                    self.def_names.insert(lhs[0].id());
                    self.pending.insert(
                        rhs[0].id(),
                        PendingName {
                            name: self.text(lhs[0]).trim().to_string(),
                            kind: "Function",
                            exported: false,
                            doc_line: doc,
                        },
                    );
                }
            }
            _ => {}
        }
    }

    fn js_exported(&self) -> bool {
        // the declaration's parent (or the declarator list's parent) is `export ...`
        let n = self.anc.len();
        (n >= 2 && self.anc[n - 2].kind == "export_statement")
            || (n >= 3
                && matches!(
                    self.anc[n - 2].kind,
                    "lexical_declaration" | "variable_declaration"
                )
                && self.anc[n - 3].kind == "export_statement")
    }

    fn js_outer_doc(&self, node: Node<'_>) -> u32 {
        if self.anc.len() > MAX_DOC_DEPTH {
            return self.doc_start_line(node);
        }
        // comments sit above `export ...` / `const ...`, not above the inner node.
        let mut outer = node;
        for _ in 0..2 {
            match outer.parent() {
                Some(p)
                    if matches!(
                        p.kind(),
                        "export_statement"
                            | "lexical_declaration"
                            | "variable_declaration"
                            | "ambient_declaration"
                    ) =>
                {
                    outer = p
                }
                _ => break,
            }
        }
        self.doc_start_line(outer)
    }

    fn declare_js(&mut self, node: Node<'_>, kind: &'static str, field: Option<&'static str>) {
        let exported = self.js_exported();
        match kind {
            "function_declaration" | "generator_function_declaration" | "function_signature" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let params = node.child_by_field_name("parameters");
                let sig = self.signature_hash(&[params, node.child_by_field_name("return_type")]);
                let arity = self.arity_of(params);
                let doc = self.js_outer_doc(node);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "Function",
                    Some(arity),
                    sig,
                    exported,
                    kind != "function_signature",
                    None,
                );
            }
            "class_declaration" | "abstract_class_declaration" | "class" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    // anonymous `export default class {}`: members still get a scope.
                    if kind == "class" && self.parent().map(|p| p.kind) == Some("export_statement")
                    {
                        let doc = self.js_outer_doc(node);
                        self.add_symbol(
                            node,
                            doc,
                            None,
                            "default".into(),
                            "Class",
                            None,
                            String::new(),
                            true,
                            true,
                            None,
                        );
                    }
                    return;
                };
                let doc = self.js_outer_doc(node);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "Class",
                    None,
                    String::new(),
                    exported,
                    true,
                    None,
                );
            }
            "method_definition" | "method_signature" | "abstract_method_signature" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let private = name.starts_with('#') || {
                    let mut c = node.walk();
                    node.children(&mut c).any(|ch| {
                        ch.kind() == "accessibility_modifier"
                            && matches!(self.text(ch).trim(), "private" | "protected")
                    })
                };
                let k = if name == "constructor" {
                    "Constructor"
                } else {
                    "Method"
                };
                let params = node.child_by_field_name("parameters");
                let sig = self.signature_hash(&[params, node.child_by_field_name("return_type")]);
                let arity = self.arity_of(params);
                let doc = self.doc_start_line(node);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    k,
                    Some(arity),
                    sig,
                    !private,
                    kind == "method_definition",
                    None,
                );
            }
            "public_field_definition" | "field_definition" | "property_signature" => {
                let name_field = if kind == "field_definition" {
                    "property"
                } else {
                    "name"
                };
                let Some((name_n, name)) = self.name_of(node, name_field) else {
                    return;
                };
                let private = name.starts_with('#') || {
                    let mut c = node.walk();
                    node.children(&mut c).any(|ch| {
                        ch.kind() == "accessibility_modifier"
                            && matches!(self.text(ch).trim(), "private" | "protected")
                    })
                };
                let doc = self.doc_start_line(node);
                if let Some(v) = node.child_by_field_name("value")
                    && is_function_value(v.kind())
                {
                    self.def_names.insert(name_n.id());
                    self.pending.insert(
                        v.id(),
                        PendingName {
                            name,
                            kind: "Method",
                            exported: !private,
                            doc_line: doc,
                        },
                    );
                    return;
                }
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "Property",
                    None,
                    String::new(),
                    !private,
                    false,
                    None,
                );
            }
            "interface_declaration" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let doc = self.js_outer_doc(node);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "Interface",
                    None,
                    String::new(),
                    exported,
                    true,
                    None,
                );
            }
            "type_alias_declaration" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let doc = self.js_outer_doc(node);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "TypeAlias",
                    None,
                    String::new(),
                    exported,
                    false,
                    None,
                );
            }
            "enum_declaration" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let doc = self.js_outer_doc(node);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "Enum",
                    None,
                    String::new(),
                    exported,
                    true,
                    None,
                );
            }
            "enum_assignment" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let doc = self.doc_start_line(node);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    "EnumMember",
                    None,
                    String::new(),
                    true,
                    false,
                    None,
                );
            }
            "property_identifier"
                if field == Some("name") && self.parent().map(|p| p.kind) == Some("enum_body") =>
            {
                let name = self.text(node).trim().to_string();
                let doc = self.doc_start_line(node);
                self.add_symbol(
                    node,
                    doc,
                    Some(node),
                    name,
                    "EnumMember",
                    None,
                    String::new(),
                    true,
                    false,
                    None,
                );
            }
            "internal_module" | "module" => {
                let Some((name_n, name)) = self.name_of(node, "name") else {
                    return;
                };
                let doc = self.js_outer_doc(node);
                self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    strip_quotes(&name),
                    "Namespace",
                    None,
                    String::new(),
                    exported,
                    true,
                    None,
                );
            }
            "variable_declarator" => {
                let Some(name_n) = node.child_by_field_name("name") else {
                    return;
                };
                if name_n.kind() != "identifier" {
                    return; // destructuring: binding references only
                }
                let name = self.text(name_n).trim().to_string();
                let value = node.child_by_field_name("value");
                let doc = self.js_outer_doc(node);
                let in_callable = self.in_callable();
                if let Some(v) = value {
                    if is_function_value(v.kind()) {
                        self.def_names.insert(name_n.id());
                        self.pending.insert(
                            v.id(),
                            PendingName {
                                name,
                                kind: "Function",
                                exported,
                                doc_line: doc,
                            },
                        );
                        return;
                    }
                    if v.kind() == "class" {
                        self.def_names.insert(name_n.id());
                        self.pending.insert(
                            v.id(),
                            PendingName {
                                name,
                                kind: "Class",
                                exported,
                                doc_line: doc,
                            },
                        );
                        return;
                    }
                }
                if in_callable {
                    return; // a local: its name is a binding reference
                }
                let is_const = node
                    .parent()
                    .and_then(|p| p.child(0))
                    .is_some_and(|k| self.text(k) == "const");
                let k = if is_const { "Const" } else { "Variable" };
                let idx = self.add_symbol(
                    node,
                    doc,
                    Some(name_n),
                    name,
                    k,
                    None,
                    String::new(),
                    exported,
                    false,
                    None,
                );
                if let Some(v) = value
                    && v.kind() == "object"
                    && let Some(i) = idx
                {
                    self.object_scopes.insert(v.id(), i);
                }
            }
            _ => {}
        }
    }

    fn maybe_import(&mut self, node: Node<'_>, kind: &'static str) {
        let line = node.start_position().row as u32 + 1;
        match (self.lang, kind) {
            (l, "import_statement") if l.is_js_family() => {
                let source = node
                    .child_by_field_name("source")
                    .map(|s| strip_quotes(self.text(s)));
                let mut c = node.walk();
                let mut any = false;
                for ch in node.named_children(&mut c) {
                    match ch.kind() {
                        "import_clause" => {
                            let Some(module) = source.clone() else {
                                continue;
                            };
                            let mut c2 = ch.walk();
                            for part in ch.named_children(&mut c2) {
                                match part.kind() {
                                    "identifier" => {
                                        any = true;
                                        self.imports.push(ImportFact {
                                            line,
                                            kind: "import".into(),
                                            module: module.clone(),
                                            name: Some("default".into()),
                                            alias: Some(self.text(part).to_string()),
                                        });
                                    }
                                    "namespace_import" => {
                                        any = true;
                                        let mut c3 = part.walk();
                                        let alias = part
                                            .named_children(&mut c3)
                                            .next()
                                            .map(|n| self.text(n).to_string());
                                        self.imports.push(ImportFact {
                                            line,
                                            kind: "import".into(),
                                            module: module.clone(),
                                            name: Some("*".into()),
                                            alias,
                                        });
                                    }
                                    "named_imports" => {
                                        let mut c3 = part.walk();
                                        for spec in part.named_children(&mut c3) {
                                            if spec.kind() != "import_specifier" {
                                                continue;
                                            }
                                            any = true;
                                            let name = spec
                                                .child_by_field_name("name")
                                                .map(|n| strip_quotes(self.text(n)));
                                            let alias = spec
                                                .child_by_field_name("alias")
                                                .map(|n| self.text(n).to_string());
                                            self.imports.push(ImportFact {
                                                line,
                                                kind: "import".into(),
                                                module: module.clone(),
                                                name,
                                                alias,
                                            });
                                        }
                                    }
                                    _ => {}
                                }
                            }
                        }
                        "import_require_clause" => {
                            let module = ch
                                .child_by_field_name("source")
                                .map(|s| strip_quotes(self.text(s)))
                                .unwrap_or_default();
                            let mut c2 = ch.walk();
                            let alias = ch
                                .named_children(&mut c2)
                                .find(|n| n.kind() == "identifier")
                                .map(|n| self.text(n).to_string());
                            any = true;
                            self.imports.push(ImportFact {
                                line,
                                kind: "require".into(),
                                module,
                                name: None,
                                alias,
                            });
                        }
                        _ => {}
                    }
                }
                if !any && let Some(module) = source {
                    self.imports.push(ImportFact {
                        line,
                        kind: "import".into(),
                        module,
                        name: None,
                        alias: None,
                    });
                }
            }
            (l, "export_statement") if l.is_js_family() => {
                let Some(src) = node.child_by_field_name("source") else {
                    return;
                };
                let module = strip_quotes(self.text(src));
                let mut c = node.walk();
                let mut any = false;
                for ch in node.named_children(&mut c) {
                    match ch.kind() {
                        "export_clause" => {
                            let mut c2 = ch.walk();
                            for spec in ch.named_children(&mut c2) {
                                if spec.kind() != "export_specifier" {
                                    continue;
                                }
                                any = true;
                                self.imports.push(ImportFact {
                                    line,
                                    kind: "reexport".into(),
                                    module: module.clone(),
                                    name: spec
                                        .child_by_field_name("name")
                                        .map(|n| strip_quotes(self.text(n))),
                                    alias: spec
                                        .child_by_field_name("alias")
                                        .map(|n| strip_quotes(self.text(n))),
                                });
                            }
                        }
                        "namespace_export" => {
                            any = true;
                            let mut c2 = ch.walk();
                            let alias = ch
                                .named_children(&mut c2)
                                .next()
                                .map(|n| strip_quotes(self.text(n)));
                            self.imports.push(ImportFact {
                                line,
                                kind: "reexport_all".into(),
                                module: module.clone(),
                                name: Some("*".into()),
                                alias,
                            });
                        }
                        _ => {}
                    }
                }
                if !any {
                    self.imports.push(ImportFact {
                        line,
                        kind: "reexport_all".into(),
                        module,
                        name: Some("*".into()),
                        alias: None,
                    });
                }
            }
            (l, "call_expression") if l.is_js_family() => {
                let Some(f) = node.child_by_field_name("function") else {
                    return;
                };
                let dynamic = f.kind() == "import";
                if !(dynamic || (f.kind() == "identifier" && self.text(f) == "require")) {
                    return;
                }
                let Some(args) = node.child_by_field_name("arguments") else {
                    return;
                };
                let mut c = args.walk();
                let Some(first) = args.named_children(&mut c).next() else {
                    return;
                };
                if first.kind() != "string" {
                    return;
                }
                self.imports.push(ImportFact {
                    line,
                    kind: if dynamic { "dynamic" } else { "require" }.into(),
                    module: strip_quotes(self.text(first)),
                    name: None,
                    alias: None,
                });
            }
            (Lang::Go, "import_spec") => {
                let Some(path) = node.child_by_field_name("path") else {
                    return;
                };
                let module = strip_quotes(self.text(path));
                let alias = node
                    .child_by_field_name("name")
                    .map(|n| self.text(n).to_string());
                let name = module.rsplit('/').next().map(str::to_string);
                self.imports.push(ImportFact {
                    line,
                    kind: "import".into(),
                    module,
                    name,
                    alias,
                });
            }
            (Lang::Rust, "use_declaration") => {
                let kind = if has_child_kind(node, "visibility_modifier") {
                    "reexport"
                } else {
                    "use"
                };
                if let Some(arg) = node.child_by_field_name("argument") {
                    self.rust_use_tree(arg, "", kind, line);
                }
            }
            (Lang::Rust, "extern_crate_declaration") => {
                if let Some((_, name)) = self.name_of(node, "name") {
                    self.imports.push(ImportFact {
                        line,
                        kind: "extern_crate".into(),
                        module: name,
                        name: None,
                        alias: None,
                    });
                }
            }
            _ => {}
        }
    }

    fn rust_use_tree(&mut self, n: Node<'_>, prefix: &str, kind: &str, line: u32) {
        let join = |a: &str, b: &str| {
            if a.is_empty() {
                b.to_string()
            } else if b.is_empty() {
                a.to_string()
            } else {
                format!("{a}::{b}")
            }
        };
        match n.kind() {
            "scoped_use_list" => {
                let path = n
                    .child_by_field_name("path")
                    .map(|p| normalize_ws(self.text(p)))
                    .unwrap_or_default();
                let full = join(prefix, &path);
                if let Some(list) = n.child_by_field_name("list") {
                    self.rust_use_tree(list, &full, kind, line);
                }
            }
            "use_list" => {
                let mut c = n.walk();
                let items: Vec<Node<'_>> = n.named_children(&mut c).collect();
                for it in items {
                    self.rust_use_tree(it, prefix, kind, line);
                }
            }
            "use_as_clause" => {
                let path = n
                    .child_by_field_name("path")
                    .map(|p| normalize_ws(self.text(p)))
                    .unwrap_or_default();
                let alias = n
                    .child_by_field_name("alias")
                    .map(|a| self.text(a).to_string());
                let full = join(prefix, &path);
                let (module, name) = match full.rsplit_once("::") {
                    Some((m, nm)) => (m.to_string(), nm.to_string()),
                    None => (String::new(), full.clone()),
                };
                self.imports.push(ImportFact {
                    line,
                    kind: kind.into(),
                    module,
                    name: Some(name),
                    alias,
                });
            }
            "use_wildcard" => {
                let text = normalize_ws(self.text(n));
                let path = text
                    .trim_end_matches('*')
                    .trim_end_matches("::")
                    .to_string();
                self.imports.push(ImportFact {
                    line,
                    kind: kind.into(),
                    module: join(prefix, &path),
                    name: Some("*".into()),
                    alias: None,
                });
            }
            _ => {
                let full = join(prefix, &normalize_ws(self.text(n)));
                let (module, name) = match full.rsplit_once("::") {
                    Some((m, nm)) => (m.to_string(), nm.to_string()),
                    None => (String::new(), full.clone()),
                };
                self.imports.push(ImportFact {
                    line,
                    kind: kind.into(),
                    module,
                    name: Some(name),
                    alias: None,
                });
            }
        }
    }

    fn finish(mut self) -> FileFacts {
        // Go methods belong to their receiver type when it is declared in this file.
        for (idx, recv) in std::mem::take(&mut self.go_receivers) {
            if let Some(t) = self.symbols.iter().position(|s| {
                s.depth == 0
                    && s.name == recv
                    && matches!(s.kind.as_str(), "Struct" | "Interface" | "TypeAlias")
            }) {
                let s = &mut self.symbols[idx as usize];
                s.container = Some(t as u32);
                s.depth = 1;
            }
        }
        if !self.exported_names.is_empty() {
            for s in self.symbols.iter_mut() {
                if s.depth == 0 && self.exported_names.contains(&s.name) {
                    s.exported = true;
                }
            }
        }
        FileFacts {
            engine: "tree_sitter".into(),
            parse_errors: self.parse_errors,
            invalid_utf8: false,
            symbols_truncated: self.truncated || self.refs_truncated,
            lexical_fallback: false,
            nesting_truncated: self.nesting_truncated,
            line_count: 0,
            names: self.names,
            symbols: self.symbols,
            refs: self.refs,
            imports: self.imports,
            chunks: Vec::new(),
        }
    }
}

fn self_parent_or(node: Node<'_>) -> Node<'_> {
    match node.parent() {
        Some(p)
            if matches!(
                p.kind(),
                "type_declaration" | "const_declaration" | "var_declaration"
            ) =>
        {
            p
        }
        _ => node,
    }
}
