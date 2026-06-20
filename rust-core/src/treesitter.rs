use std::collections::HashSet;
use std::path::Path;

use streaming_iterator::StreamingIterator;
use tree_sitter::{Language, Parser, Query, QueryCursor};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TsSymbol {
    pub symbol: String,
    pub kind: String,
    pub line_start: usize,
    pub line_end: usize,
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
    language: Language,
    query: &'static str,
}

/// Extract semantic symbol candidates from a file using tree-sitter.
pub fn extract_symbols(relative_path: &str, source: &str) -> Option<Vec<TsSymbol>> {
    let config = language_config(relative_path)?;
    let mut parser = Parser::new();
    if parser.set_language(&config.language).is_err() {
        return Some(Vec::new());
    }
    let tree = match parser.parse(source, None) {
        Some(tree) => tree,
        None => return Some(Vec::new()),
    };
    let query = match Query::new(&config.language, config.query) {
        Ok(query) => query,
        Err(_) => return Some(Vec::new()),
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
            if !seen.insert((symbol.to_string(), line_start)) {
                continue;
            }
            // a symbol is never its own scope (e.g. the `impl Foo` type capture).
            let enclosing_scope =
                enclosing_scope_of(capture.node, source.as_bytes()).filter(|s| s != symbol);
            symbols.push(TsSymbol {
                symbol: symbol.to_string(),
                kind: kind.to_string(),
                line_start,
                line_end,
                enclosing_scope,
            });
            if symbols.len() >= 64 {
                return Some(symbols);
            }
        }
    }
    Some(symbols)
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
            language: tree_sitter_rust::LANGUAGE.into(),
            query: RUST_QUERY,
        }),
        "go" => Some(LanguageConfig {
            language: tree_sitter_go::LANGUAGE.into(),
            query: GO_QUERY,
        }),
        "ts" => Some(LanguageConfig {
            language: tree_sitter_typescript::LANGUAGE_TYPESCRIPT.into(),
            query: TYPESCRIPT_QUERY,
        }),
        "tsx" => Some(LanguageConfig {
            language: tree_sitter_typescript::LANGUAGE_TSX.into(),
            query: TYPESCRIPT_QUERY,
        }),
        "js" | "mjs" | "cjs" => Some(LanguageConfig {
            language: tree_sitter_javascript::LANGUAGE.into(),
            query: JAVASCRIPT_QUERY,
        }),
        "jsx" => Some(LanguageConfig {
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
    use super::{extract_symbols, TsSymbol};

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
        assert!(list.iter().any(|(name, kind)| *name == "build" && *kind == "method"));
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
