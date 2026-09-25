use std::cell::RefCell;
use std::collections::{HashMap, HashSet};
use std::path::Path;
use std::rc::Rc;

use streaming_iterator::StreamingIterator;
use tree_sitter::{Language, Parser, Query, QueryCursor};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TsSymbol {
    pub symbol: String,
    pub kind: String,
    pub line_start: usize,
    pub line_end: usize,
    /// 0-based column of the symbol NAME on its start line — the LSP position used
    /// to resolve references/definition for this symbol.
    pub name_column: usize,
    /// Name of the nearest enclosing container (impl/class/module/trait/fn), e.g.
    /// "impl AuthMiddleware" → "AuthMiddleware". None at top level.
    pub enclosing_scope: Option<String>,
}

// Container AST node kinds a symbol can be nested INSIDE. Deliberately excludes
// function/struct/enum/type definitions so a symbol never reports its own
// definition node as its scope — only true containers (impl/class/trait/module).
const SCOPE_NODE_KINDS: &[&str] = &[
    "impl_item",
    "trait_item",
    "mod_item",
    "class_declaration",
    "interface_declaration",
    "namespace_declaration",
];

// Walk up from a node to the nearest enclosing named scope, returning its name
// (the `name`/`type` child's text). Skips the symbol's own definition node.
fn enclosing_scope_of(node: tree_sitter::Node, source: &[u8]) -> Option<String> {
    let mut cur = node.parent();
    while let Some(n) = cur {
        if SCOPE_NODE_KINDS.contains(&n.kind()) {
            // prefer a `name` child, else a `type` child (Rust impl blocks).
            for field in ["name", "type"] {
                if let Some(named) = n.child_by_field_name(field) {
                    if let Ok(text) = named.utf8_text(source) {
                        let text = text.trim();
                        if !text.is_empty() {
                            return Some(text.to_string());
                        }
                    }
                }
            }
        }
        cur = n.parent();
    }
    None
}

struct LanguageConfig {
    /// Grammar identity for the compiled-query cache.
    name: &'static str,
    language: Language,
    query: &'static str,
}

thread_local! {
    /// Compiled queries per grammar. Compiling a query costs far more than parsing a
    /// typical file, so it is done once per grammar per thread, not once per file.
    static QUERY_CACHE: RefCell<HashMap<&'static str, Rc<Query>>> = RefCell::new(HashMap::new());
}

fn compiled_query(config: &LanguageConfig) -> Option<Rc<Query>> {
    QUERY_CACHE.with(|cache| {
        if let Some(q) = cache.borrow().get(config.name) {
            return Some(q.clone());
        }
        let q = Rc::new(Query::new(&config.language, config.query).ok()?);
        cache.borrow_mut().insert(config.name, q.clone());
        Some(q)
    })
}

/// Index-completeness bound on symbols extracted from one file. Display limits are
/// applied by callers; reaching this bound is reported as `symbols_truncated`.
pub const MAX_SYMBOLS_PER_FILE: usize = 4096;

/// Whether `relative_path` has a tree-sitter grammar.
pub fn supports(relative_path: &str) -> bool {
    language_config(relative_path).is_some()
}

/// Extract semantic symbol candidates from a file using tree-sitter, up to
/// MAX_SYMBOLS_PER_FILE.
pub fn extract_symbols(relative_path: &str, source: &str) -> Option<Vec<TsSymbol>> {
    extract_symbols_limited(relative_path, source, MAX_SYMBOLS_PER_FILE).map(|(s, _)| s)
}

/// Extract at most `max` symbols; the flag is true when more symbols existed.
pub fn extract_symbols_limited(
    relative_path: &str,
    source: &str,
    max: usize,
) -> Option<(Vec<TsSymbol>, bool)> {
    let config = language_config(relative_path)?;
    let mut parser = Parser::new();
    if parser.set_language(&config.language).is_err() {
        return Some((Vec::new(), false));
    }
    let tree = match parser.parse(source, None) {
        Some(tree) => tree,
        None => return Some((Vec::new(), false)),
    };
    let query = match compiled_query(&config) {
        Some(query) => query,
        None => return Some((Vec::new(), false)),
    };
    let mut cursor = QueryCursor::new();
    let mut symbols = Vec::new();
    let mut seen = HashSet::<(String, usize)>::new();
    let root_node = tree.root_node();
    let capture_names = query.capture_names();
    let mut matches = cursor.matches(&query, root_node, source.as_bytes());
    while let Some(mat) = matches.next() {
        for capture in mat.captures {
            let Some(capture_name) = capture_names.get(capture.index as usize).copied() else {
                continue;
            };
            let Some((_, kind)) = capture_name.split_once('.') else {
                continue;
            };
            let Some(kind) = map_capture_kind(kind) else {
                continue;
            };
            let symbol = match capture.node.utf8_text(source.as_bytes()) {
                Ok(value) => value,
                Err(_) => continue,
            };
            let symbol = symbol.trim();
            if symbol.is_empty() {
                continue;
            }
            let line_start = capture.node.start_position().row + 1;
            let line_end = capture.node.end_position().row + 1;
            let name_column = capture.node.start_position().column;
            if !seen.insert((symbol.to_string(), line_start)) {
                continue;
            }
            if symbols.len() >= max {
                return Some((symbols, true));
            }
            // a symbol is never its own scope (e.g. the `impl Foo` type capture).
            let enclosing_scope =
                enclosing_scope_of(capture.node, source.as_bytes()).filter(|s| s != symbol);
            symbols.push(TsSymbol {
                symbol: symbol.to_string(),
                kind: kind.to_string(),
                line_start,
                line_end,
                name_column,
                enclosing_scope,
            });
        }
    }
    Some((symbols, false))
}

fn map_capture_kind(kind: &str) -> Option<&'static str> {
    match kind {
        "function" => Some("function"),
        "method" => Some("method"),
        "type" => Some("type"),
        "class" => Some("class"),
        "struct" => Some("type"),
        "enum" => Some("type"),
        "trait" => Some("type"),
        "interface" => Some("type"),
        _ => None,
    }
}

fn language_config(relative_path: &str) -> Option<LanguageConfig> {
    let extension = Path::new(relative_path)
        .extension()
        .and_then(|item| item.to_str())?
        .to_ascii_lowercase();
    match extension.as_str() {
        "rs" => Some(LanguageConfig {
            name: "rust",
            language: tree_sitter_rust::LANGUAGE.into(),
            query: RUST_QUERY,
        }),
        "go" => Some(LanguageConfig {
            name: "go",
            language: tree_sitter_go::LANGUAGE.into(),
            query: GO_QUERY,
        }),
        "ts" => Some(LanguageConfig {
            name: "typescript",
            language: tree_sitter_typescript::LANGUAGE_TYPESCRIPT.into(),
            query: TYPESCRIPT_QUERY,
        }),
        "tsx" => Some(LanguageConfig {
            name: "tsx",
            language: tree_sitter_typescript::LANGUAGE_TSX.into(),
            query: TYPESCRIPT_QUERY,
        }),
        "js" | "mjs" | "cjs" => Some(LanguageConfig {
            name: "javascript",
            language: tree_sitter_javascript::LANGUAGE.into(),
            query: JAVASCRIPT_QUERY,
        }),
        "jsx" => Some(LanguageConfig {
            name: "javascript",
            language: tree_sitter_javascript::LANGUAGE.into(),
            query: JAVASCRIPT_QUERY,
        }),
        _ => None,
    }
}

const RUST_QUERY: &str = r#"
(function_item
  name: (identifier) @name.function)

(impl_item
  (declaration_list
    (function_item
      name: (identifier) @name.method)))

(struct_item
  name: (type_identifier) @name.type)

(enum_item
  name: (type_identifier) @name.type)

(trait_item
  name: (type_identifier) @name.type)

(impl_item
  type: (type_identifier) @name.type)
"#;

const TYPESCRIPT_QUERY: &str = r#"
(function_declaration
  name: (identifier) @name.function)

(class_declaration
  name: (type_identifier) @name.class)

(interface_declaration
  name: (type_identifier) @name.interface)

(type_alias_declaration
  name: (type_identifier) @name.type)

(method_definition
  name: (property_identifier) @name.method)
"#;

const JAVASCRIPT_QUERY: &str = r#"
(function_declaration
  name: (identifier) @name.function)

(class_declaration
  name: (identifier) @name.class)

(method_definition
  name: (property_identifier) @name.method)
"#;

const GO_QUERY: &str = r#"
(function_declaration
  name: (identifier) @name.function)

(method_declaration
  name: (field_identifier) @name.method)

(type_declaration
  (type_spec
    name: (type_identifier) @name.type))
"#;

#[cfg(test)]
mod tests {
    use super::{TsSymbol, extract_symbols, extract_symbols_limited};

    #[test]
    fn extraction_is_complete_past_old_cap_and_reports_truncation() {
        let source: String = (1..=70)
            .map(|i| format!("fn symbol_{i:03}() {{}}\n"))
            .collect();
        let all = extract_symbols("many.rs", &source).unwrap();
        assert_eq!(all.len(), 70);
        assert_eq!(all[69].symbol, "symbol_070");
        let (some, truncated) = extract_symbols_limited("many.rs", &source, 10).unwrap();
        assert_eq!(some.len(), 10);
        assert!(truncated);
        let (exact, t2) = extract_symbols_limited("many.rs", &source, 70).unwrap();
        assert_eq!(exact.len(), 70);
        assert!(!t2);
    }

    fn read_lines(symbols: &[TsSymbol]) -> Vec<(&str, &str)> {
        symbols
            .iter()
            .map(|item| (item.symbol.as_str(), item.kind.as_str()))
            .collect::<Vec<_>>()
    }

    #[test]
    fn extracts_rust_symbols_with_treesitter() {
        let source = r#"
            pub fn foo() {}

            struct Bar {}
            enum Baz {}
            trait Qux {}

            impl Bar {
                pub fn build(self) {}
            }
        "#;
        let symbols = extract_symbols("src/main.rs", source).expect("rust is supported");
        let list = read_lines(&symbols);

        assert!(list.contains(&("foo", "function")));
        assert!(list.contains(&("Bar", "type")));
        assert!(list.contains(&("Baz", "type")));
        assert!(list.contains(&("Qux", "type")));
        assert!(
            list.iter()
                .any(|(name, kind)| *name == "build" && *kind == "method")
        );
        // the method inside `impl Bar` carries its enclosing scope; top-level fn does not.
        let build = symbols.iter().find(|s| s.symbol == "build").unwrap();
        assert_eq!(build.enclosing_scope.as_deref(), Some("Bar"));
        let foo = symbols.iter().find(|s| s.symbol == "foo").unwrap();
        assert_eq!(foo.enclosing_scope, None);
    }

    #[test]
    fn extracts_go_symbols_with_treesitter() {
        let source = r#"
            package main

            type Server struct {}

            func Handler(name string) string { return name }
        "#;
        let symbols = extract_symbols("cmd/main.go", source).expect("go is supported");
        let list = read_lines(&symbols);

        assert!(list.contains(&("Server", "type")));
        assert!(list.contains(&("Handler", "function")));
    }

    #[test]
    fn extracts_typescript_symbols_with_treesitter() {
        let source = r#"
            export function render() {}
            interface Props {}
            class Widget {}
        "#;
        let symbols = extract_symbols("ui/widget.ts", source).expect("typescript is supported");
        let list = read_lines(&symbols);

        assert!(list.contains(&("render", "function")));
        assert!(list.contains(&("Props", "type")));
        assert!(list.contains(&("Widget", "class")));
    }
}
