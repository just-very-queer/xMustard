//! Legacy repo-map symbol extraction (`repomap`, `symbolgraph`) over the language packs
//! (`index::lang`): each pack's tag query yields `@definition.*` matches, mapped to the
//! legacy kinds (`function`, `method`, `class`, `type`).

use std::collections::{HashMap, HashSet};

use streaming_iterator::StreamingIterator;
use tree_sitter::{Parser, QueryCursor};

use crate::index::extract::within_parse_bounds;
use crate::index::lang::{self, Lang};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TsSymbol {
    pub symbol: String,
    pub kind: String,
    pub line_start: usize,
    pub line_end: usize,
    /// 0-based column of the symbol NAME on its start line — the LSP position used
    /// to resolve references/definition for this symbol.
    pub name_column: usize,
    /// Name of the nearest enclosing container (impl/class/module/trait/namespace), e.g.
    /// "impl AuthMiddleware" → "AuthMiddleware". None at top level; functions are never
    /// scopes.
    pub enclosing_scope: Option<String>,
}

/// Index-completeness bound on symbols extracted from one file. Display limits are
/// applied by callers; reaching this bound is reported as `symbols_truncated`.
pub const MAX_SYMBOLS_PER_FILE: usize = 4096;

/// Whether this build has a tree-sitter grammar for `relative_path`.
pub fn supports(relative_path: &str) -> bool {
    Lang::for_path(relative_path).is_some_and(Lang::has_grammar)
}

/// Extract semantic symbol candidates from a file using tree-sitter, up to
/// MAX_SYMBOLS_PER_FILE.
pub fn extract_symbols(relative_path: &str, source: &str) -> Option<Vec<TsSymbol>> {
    extract_symbols_limited(relative_path, source, MAX_SYMBOLS_PER_FILE).map(|(s, _)| s)
}

/// Extract at most `max` symbols; the flag is true when more symbols existed. None when
/// the file's language has no grammar in this build, or when the file is past the
/// index's parse bounds (size, tokens, nesting): the caller then uses its regexes.
pub fn extract_symbols_limited(
    relative_path: &str,
    source: &str,
    max: usize,
) -> Option<(Vec<TsSymbol>, bool)> {
    let lang = Lang::for_path(relative_path)?;
    if !within_parse_bounds(lang, source.as_bytes()) {
        return None;
    }
    let grammar = lang.grammar()?;
    let mut parser = Parser::new();
    if parser.set_language(&grammar).is_err() {
        return Some((Vec::new(), false));
    }
    let Some(tree) = parser.parse(source, None) else {
        return Some((Vec::new(), false));
    };
    let Some(query) = lang::tag_query(lang) else {
        return Some((Vec::new(), false));
    };
    let src = source.as_bytes();
    let capture_names = query.capture_names();
    // definition node id -> (label, name) for every match, so scopes can be looked up
    let mut defs: Vec<(tree_sitter::Node<'_>, tree_sitter::Node<'_>, &str)> = Vec::new();
    let mut containers: HashMap<usize, &str> = HashMap::new();
    // every definition's label: the nearest one decides whether a function is a method
    let mut labels: HashMap<usize, &str> = HashMap::new();
    let mut cursor = QueryCursor::new();
    let mut matches = cursor.matches(&query, tree.root_node(), src);
    while let Some(mat) = matches.next() {
        let (mut def, mut name) = (None, None);
        for c in mat.captures {
            match capture_names[c.index as usize].split_once('.') {
                Some(("definition", label)) => def = Some((c.node, label)),
                None if capture_names[c.index as usize] == "name" => name = Some(c.node),
                _ => {}
            }
        }
        let (Some((node, label)), Some(name)) = (def, name) else {
            continue;
        };
        let Ok(text) = name.utf8_text(src) else {
            continue;
        };
        labels.entry(node.id()).or_insert(label);
        if lang::is_container(label) {
            containers.entry(node.id()).or_insert(text.trim());
        }
        defs.push((node, name, label));
    }
    let mut symbols = Vec::new();
    let mut seen = HashSet::<(String, usize)>::new();
    for (node, name, label) in defs {
        let Some(kind) = lang::legacy_kind(label) else {
            continue;
        };
        let symbol = name.utf8_text(src).unwrap_or("").trim();
        if symbol.is_empty() {
            continue;
        }
        let line_start = name.start_position().row + 1;
        if !seen.insert((symbol.to_string(), line_start)) {
            continue;
        }
        if symbols.len() >= max {
            return Some((symbols, true));
        }
        // walk from the parent so a symbol is never its own scope, but a constructor
        // named like its class still gets the class
        let ancestors = || std::iter::successors(node.parent(), |n| n.parent());
        let enclosing_scope = ancestors()
            .find_map(|n| containers.get(&n.id()))
            .map(|s| s.to_string());
        // the same method rule as the index (`tags`): a function whose nearest enclosing
        // definition is a type or module is a method
        let owner = ancestors()
            .find_map(|n| labels.get(&n.id()))
            .and_then(|l| lang::fact_kind(l));
        let kind = match (kind, owner) {
            ("function", Some(o)) if lang::METHOD_OWNERS.contains(&o) => "method",
            (kind, _) => kind,
        };
        symbols.push(TsSymbol {
            symbol: symbol.to_string(),
            kind: kind.to_string(),
            line_start,
            line_end: name.end_position().row + 1,
            name_column: name.start_position().column,
            enclosing_scope,
        });
    }
    Some((symbols, false))
}

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

    fn find<'a>(symbols: &'a [TsSymbol], name: &str) -> &'a TsSymbol {
        symbols.iter().find(|s| s.symbol == name).unwrap()
    }

    #[cfg(all(feature = "lang-java", feature = "lang-cpp"))]
    #[test]
    fn constructor_named_like_its_class_keeps_the_class_scope() {
        let java = "class Sample {\n  Sample(int n) {}\n}\n";
        let symbols = extract_symbols("Sample.java", java).unwrap();
        let ctor = symbols
            .iter()
            .find(|s| s.symbol == "Sample" && s.kind == "method")
            .unwrap();
        assert_eq!(ctor.enclosing_scope.as_deref(), Some("Sample"));
        let cpp = "class Shape {\n public:\n  Shape() {}\n  int area() { return 0; }\n};\n";
        let symbols = extract_symbols("shape.cpp", cpp).unwrap();
        let ctor = symbols
            .iter()
            .find(|s| s.symbol == "Shape" && s.kind == "method")
            .unwrap();
        assert_eq!(ctor.enclosing_scope.as_deref(), Some("Shape"));
        // in-class functions are methods, as in the index's tag extractor
        assert_eq!(find(&symbols, "area").kind, "method");
    }

    #[cfg(feature = "lang-ruby")]
    #[test]
    fn ruby_functions_in_a_class_are_methods() {
        let rb = "def top; end\nclass Widget\n  def initialize; end\nend\n";
        let symbols = extract_symbols("w.rb", rb).unwrap();
        assert_eq!(find(&symbols, "initialize").kind, "method");
        assert_eq!(find(&symbols, "top").kind, "function");
    }

    #[test]
    fn files_past_the_parse_bounds_are_not_parsed() {
        let over_size: String = (0..6000).map(|i| format!("fn f{i}() {{}}\n")).collect();
        assert!(over_size.len() > crate::index::extract::DEFAULT_MAX_PARSE_BYTES);
        assert!(extract_symbols("big.rs", &over_size).is_none());
        let deep = format!("fn f() {{ {} }}\n", "(".repeat(300) + &")".repeat(300));
        assert!(extract_symbols("deep.rs", &deep).is_none());
        // the repo-map entry point falls back to its regexes for them
        let out = crate::repomap::extract_source_symbols("big.rs", &over_size, 10);
        assert_eq!(out.engine, "regex");
        assert!(out.truncated);
        // and a refused file the regexes find nothing in still reports the regex engine,
        // so graph coverage counts it as failed rather than supported
        let blank = format!("// {}\n", "x".repeat(70_000));
        assert_eq!(
            crate::repomap::extract_source_symbols("blank.rs", &blank, 10).engine,
            "regex"
        );
    }
}
