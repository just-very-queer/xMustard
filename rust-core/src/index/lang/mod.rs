//! Language provider contract (PAR-SYM-05, WS-16).
//!
//! Every indexed language is one [`Pack`] row in [`PACKS`]: its extensions, grammar,
//! tag query, identifier node kinds, export rule and lexical fallback patterns. Callers
//! ask the table (`Lang::for_path`, `Lang::pack`) instead of branching per language.
//!
//! Go, Rust, TypeScript/TSX and JavaScript keep the hand-written WS-07 walker for their
//! per-file facts ([`Extractor::Walker`]); every other pack is extracted by the generic
//! tag-query engine in [`tags`] ([`Extractor::Tags`]). Both feed the same `FileFacts`.
//!
//! Grammars of the added packs are feature-gated (D-22): each `lang-*` Cargo feature
//! pulls in one grammar crate, and `lang-all` (the default) ships them all. A pack whose
//! feature is off keeps its extensions and lexical patterns, so its files are still
//! indexed lexically and reported as `unsupported` in the per-language coverage.
//!
//! ## Unified capture tags
//!
//! Tag queries (`queries/<lang>.scm`) use one vocabulary for every language:
//! - `@definition.<label>` on a declaration node, `@name` on its name node and
//!   optionally `@params` on its parameter list (without one, the declaration's own
//!   `parameter` children are the parameters). Labels: function, method,
//!   constructor, class, struct, interface, trait, enum, enum_member, module,
//!   namespace, type, impl, field, constant, variable, macro.
//! - `@reference.<kind>` directly on an identifier node: call, member_call, member,
//!   type, extends, implements, write, bind. Identifiers without a tag are reads.
//! - `@import.<kind>` on an import statement, `@module` on the module it names and
//!   optionally `@imported` and `@alias`. Kinds carry the import semantics: `import`
//!   (named), `wildcard` (every exported name), `namespace` (a qualifier bound to the
//!   module), `require` (a file load) and `include` (textual inclusion).
//!
//! Queries are original to this repository; the capture vocabulary follows the MIT
//! tree-sitter `tags.scm` convention. The provenance line heads each query file.

mod bash;
mod c;
mod cpp;
mod csharp;
mod java;
mod kotlin;
mod php;
mod python;
mod ruby;
mod swift;
pub mod tags;

use std::cell::RefCell;
use std::collections::HashMap;
use std::path::Path;
use std::rc::Rc;

use tree_sitter::{Language, Query};

/// An indexed language. The discriminant is the language's row in [`PACKS`].
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Lang {
    Rust,
    Go,
    TypeScript,
    Tsx,
    JavaScript,
    Python,
    Java,
    Ruby,
    C,
    Cpp,
    CSharp,
    Php,
    Kotlin,
    Swift,
    Bash,
}

/// How a pack's per-file facts are extracted when its grammar parses the file.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Extractor {
    /// The hand-written WS-07 walker (Go, Rust, TypeScript/TSX, JavaScript).
    Walker,
    /// The generic tag-query engine (`tags::extract`).
    Tags,
}

/// Which declarations count as exported, judged from the declaration's header (the
/// source between its start and its name: modifiers, keywords, return type).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Export {
    /// A name without a leading underscore (Python convention).
    NoUnderscore,
    /// Exported only when the header names one of these words (Java, C#).
    Requires(&'static [&'static str]),
    /// Exported unless the header names one of these words (C `static`, Kotlin
    /// `private`); an empty list exports everything.
    Unless(&'static [&'static str]),
}

impl Export {
    pub fn exported(self, name: &str, header: &str) -> bool {
        let words = || header.split(|c: char| !(c.is_alphanumeric() || c == '_'));
        match self {
            Export::NoUnderscore => !name.starts_with('_'),
            Export::Requires(any) => words().any(|w| any.contains(&w)),
            Export::Unless(none) => !words().any(|w| none.contains(&w)),
        }
    }
}

/// One language provider.
pub struct Pack {
    pub lang: Lang,
    pub name: &'static str,
    /// Lower-case file extensions, without the dot.
    pub extensions: &'static [&'static str],
    /// Grammar crate identity, part of the analyzer and parser versions.
    pub grammar_id: &'static str,
    /// None when the pack's grammar feature is off in this build.
    pub grammar: Option<fn() -> Language>,
    pub extractor: Extractor,
    /// Tag query in the unified capture vocabulary (see the module docs).
    pub tags: &'static str,
    /// Node kinds that are identifier occurrences (reference candidates).
    pub ident_kinds: &'static [&'static str],
    pub export: Export,
    /// Lexical fallback: (line pattern whose first group is the declared name, kind).
    /// Compiled once per process (`lexical::patterns`).
    pub lexical: &'static [(&'static str, &'static str)],
}

/// Declares `$name: Option<fn() -> Language>`, Some only when `$feature` is enabled.
macro_rules! gated_grammar {
    ($name:ident, $feature:literal, $language:expr) => {
        #[cfg(feature = $feature)]
        pub(super) const $name: Option<fn() -> tree_sitter::Language> = Some(|| $language.into());
        #[cfg(not(feature = $feature))]
        pub(super) const $name: Option<fn() -> tree_sitter::Language> = None;
    };
}
pub(crate) use gated_grammar;

/// Every pack, in `Lang` discriminant order (checked by a test).
pub static PACKS: [&Pack; 15] = [
    &RUST,
    &GO,
    &TYPESCRIPT,
    &TSX,
    &JAVASCRIPT,
    &python::PACK,
    &java::PACK,
    &ruby::PACK,
    &c::PACK,
    &cpp::PACK,
    &csharp::PACK,
    &php::PACK,
    &kotlin::PACK,
    &swift::PACK,
    &bash::PACK,
];

impl Lang {
    pub fn pack(self) -> &'static Pack {
        PACKS[self as usize]
    }

    pub fn for_path(path: &str) -> Option<Lang> {
        let ext = Path::new(path).extension()?.to_str()?.to_ascii_lowercase();
        PACKS
            .iter()
            .find(|p| p.extensions.contains(&ext.as_str()))
            .map(|p| p.lang)
    }

    pub fn name(self) -> &'static str {
        self.pack().name
    }

    pub fn grammar(self) -> Option<Language> {
        self.pack().grammar.map(|g| g())
    }

    /// Whether this build parses the language with tree-sitter.
    pub fn has_grammar(self) -> bool {
        self.pack().grammar.is_some()
    }

    pub fn is_js_family(self) -> bool {
        matches!(self, Lang::TypeScript | Lang::Tsx | Lang::JavaScript)
    }
}

/// Grammar identities of the packs compiled into this build, `;`-joined in table order.
pub fn grammar_ids() -> String {
    let mut ids: Vec<&str> = PACKS
        .iter()
        .filter(|p| p.grammar.is_some())
        .map(|p| p.grammar_id)
        .collect();
    ids.dedup();
    ids.join(";")
}

/// How well one file's language was served.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Support {
    /// Extracted with its grammar.
    Supported,
    /// Its pack has no grammar in this build: lexical symbols and references only.
    Unsupported,
    /// Its grammar is compiled in, but the file was extracted lexically (past the parse
    /// bounds) or parsed with errors.
    Failed,
}

/// Per-language file counts by [`Support`], reported in index and graph coverage.
#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct LanguageCoverage {
    pub supported: usize,
    pub unsupported: usize,
    pub failed: usize,
}

impl LanguageCoverage {
    pub fn add(&mut self, support: Support) {
        *match support {
            Support::Supported => &mut self.supported,
            Support::Unsupported => &mut self.unsupported,
            Support::Failed => &mut self.failed,
        } += 1;
    }
}

thread_local! {
    /// Compiled tag queries per language. Compiling a query costs far more than parsing
    /// a typical file, so it happens once per language per thread.
    static QUERY_CACHE: RefCell<HashMap<Lang, Rc<Query>>> = RefCell::new(HashMap::new());
}

/// The pack's compiled tag query; None without a grammar (or for a query that fails to
/// compile, which the fixture tests rule out).
pub fn tag_query(lang: Lang) -> Option<Rc<Query>> {
    QUERY_CACHE.with(|cache| {
        if let Some(q) = cache.borrow().get(&lang) {
            return Some(q.clone());
        }
        let q = Rc::new(Query::new(&lang.grammar()?, lang.pack().tags).ok()?);
        cache.borrow_mut().insert(lang, q.clone());
        Some(q)
    })
}

/// `FileFacts` symbol kind for a `@definition.<label>`.
pub fn fact_kind(label: &str) -> Option<&'static str> {
    Some(match label {
        "function" => "Function",
        "method" => "Method",
        "constructor" => "Constructor",
        "class" => "Class",
        "struct" => "Struct",
        "interface" => "Interface",
        "trait" => "Trait",
        "enum" => "Enum",
        "enum_member" => "EnumMember",
        "module" => "Module",
        "namespace" => "Namespace",
        "type" => "TypeAlias",
        "impl" => "Impl",
        "field" => "Property",
        "constant" => "Const",
        "variable" => "Variable",
        "macro" => "Macro",
        _ => return None,
    })
}

/// Legacy repo-map kind (`function`, `method`, `class`, `type`) for a label; None for
/// labels the legacy symbol list never carried (they still act as scopes).
pub fn legacy_kind(label: &str) -> Option<&'static str> {
    Some(match label {
        "function" => "function",
        "method" | "constructor" => "method",
        "class" => "class",
        "struct" | "enum" | "trait" | "interface" | "type" => "type",
        _ => return None,
    })
}

/// Labels whose declarations contain other declarations (a method's owner, an
/// enclosing scope).
pub fn is_container(label: &str) -> bool {
    matches!(
        label,
        "class"
            | "struct"
            | "interface"
            | "trait"
            | "enum"
            | "type"
            | "impl"
            | "module"
            | "namespace"
    )
}

// ---- the WS-07 walker languages ----

const JS_IDENTS: &[&str] = &[
    "identifier",
    "type_identifier",
    "property_identifier",
    "shorthand_property_identifier",
    "shorthand_property_identifier_pattern",
    "private_property_identifier",
];

const JS_LEXICAL: &[(&str, &str)] = &[
    (
        r"^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:async\s+)?function\*?\s+([A-Za-z_$][\w$]*)",
        "Function",
    ),
    (
        r"^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)",
        "Class",
    ),
    (
        r"^\s*(?:export\s+)?(?:declare\s+)?interface\s+([A-Za-z_$][\w$]*)",
        "Interface",
    ),
    (
        r"^\s*(?:export\s+)?(?:declare\s+)?type\s+([A-Za-z_$][\w$]*)\s*(?:<[^=]*>)?\s*=",
        "TypeAlias",
    ),
    (
        r"^\s*(?:export\s+)?(?:declare\s+)?(?:const\s+)?enum\s+([A-Za-z_$][\w$]*)",
        "Enum",
    ),
    (
        r"^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]*)?=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*(?::[^=]*)?=>|[A-Za-z_$][\w$]*\s*=>)",
        "Function",
    ),
    (
        r"^\s+(?:(?:public|private|protected|static|async|readonly|override|abstract|get|set)\s+)*([A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\([^)]*\)\s*(?::\s*[^{;]+)?\{\s*$",
        "Method",
    ),
];

static RUST: Pack = Pack {
    lang: Lang::Rust,
    name: "rust",
    extensions: &["rs"],
    grammar_id: "rust-0.24",
    grammar: Some(|| tree_sitter_rust::LANGUAGE.into()),
    extractor: Extractor::Walker,
    tags: include_str!("queries/rust.scm"),
    ident_kinds: &[
        "identifier",
        "type_identifier",
        "field_identifier",
        "shorthand_field_identifier",
    ],
    export: Export::Requires(&["pub"]),
    lexical: &[
        (
            r"^\s*(?:pub(?:\([^)]*\))?\s+)?(?:const\s+)?(?:async\s+)?(?:unsafe\s+)?(?:extern\s+\S+\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)",
            "Function",
        ),
        (
            r"^\s*(?:pub(?:\([^)]*\))?\s+)?struct\s+([A-Za-z_][A-Za-z0-9_]*)",
            "Struct",
        ),
        (
            r"^\s*(?:pub(?:\([^)]*\))?\s+)?enum\s+([A-Za-z_][A-Za-z0-9_]*)",
            "Enum",
        ),
        (
            r"^\s*(?:pub(?:\([^)]*\))?\s+)?(?:unsafe\s+)?trait\s+([A-Za-z_][A-Za-z0-9_]*)",
            "Trait",
        ),
        (
            r"^\s*(?:pub(?:\([^)]*\))?\s+)?type\s+([A-Za-z_][A-Za-z0-9_]*)",
            "TypeAlias",
        ),
    ],
};

static GO: Pack = Pack {
    lang: Lang::Go,
    name: "go",
    extensions: &["go"],
    grammar_id: "go-0.25",
    grammar: Some(|| tree_sitter_go::LANGUAGE.into()),
    extractor: Extractor::Walker,
    tags: include_str!("queries/go.scm"),
    ident_kinds: &[
        "identifier",
        "type_identifier",
        "field_identifier",
        "package_identifier",
    ],
    export: Export::Unless(&[]),
    lexical: &[
        (
            r"^\s*func\s+(?:\([^)]+\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*[\[(]",
            "Function",
        ),
        (r"^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+struct\b", "Struct"),
        (
            r"^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+interface\b",
            "Interface",
        ),
        (r"^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\b", "TypeAlias"),
    ],
};

static TYPESCRIPT: Pack = Pack {
    lang: Lang::TypeScript,
    name: "typescript",
    extensions: &["ts", "mts", "cts"],
    grammar_id: "typescript-0.23",
    grammar: Some(|| tree_sitter_typescript::LANGUAGE_TYPESCRIPT.into()),
    extractor: Extractor::Walker,
    tags: include_str!("queries/typescript.scm"),
    ident_kinds: JS_IDENTS,
    export: Export::Requires(&["export"]),
    lexical: JS_LEXICAL,
};

static TSX: Pack = Pack {
    lang: Lang::Tsx,
    name: "tsx",
    extensions: &["tsx"],
    grammar_id: "typescript-0.23",
    grammar: Some(|| tree_sitter_typescript::LANGUAGE_TSX.into()),
    extractor: Extractor::Walker,
    tags: include_str!("queries/typescript.scm"),
    ident_kinds: JS_IDENTS,
    export: Export::Requires(&["export"]),
    lexical: JS_LEXICAL,
};

static JAVASCRIPT: Pack = Pack {
    lang: Lang::JavaScript,
    name: "javascript",
    extensions: &["js", "jsx", "mjs", "cjs"],
    grammar_id: "javascript-0.25",
    grammar: Some(|| tree_sitter_javascript::LANGUAGE.into()),
    extractor: Extractor::Walker,
    tags: include_str!("queries/javascript.scm"),
    ident_kinds: JS_IDENTS,
    export: Export::Requires(&["export"]),
    lexical: JS_LEXICAL,
};

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn packs_are_indexed_by_lang() {
        for (i, p) in PACKS.iter().enumerate() {
            assert_eq!(p.lang as usize, i, "{} is out of order", p.name);
        }
    }

    #[test]
    fn extensions_resolve_to_one_pack() {
        let mut seen = std::collections::HashSet::new();
        for p in PACKS.iter() {
            for e in p.extensions {
                assert!(seen.insert(*e), "extension {e} is claimed twice");
                assert_eq!(Lang::for_path(&format!("a/b.{e}")), Some(p.lang));
                assert_eq!(
                    Lang::for_path(&format!("B.{}", e.to_uppercase())),
                    Some(p.lang)
                );
            }
        }
        assert_eq!(Lang::for_path("README.md"), None);
        assert_eq!(Lang::for_path("Makefile"), None);
    }

    #[test]
    fn every_compiled_grammar_compiles_its_tag_query() {
        for p in PACKS.iter().filter(|p| p.grammar.is_some()) {
            let lang = (p.grammar.unwrap())();
            if let Err(e) = Query::new(&lang, p.tags) {
                panic!("{} tag query: {e}", p.name);
            }
        }
    }

    #[test]
    fn export_rules() {
        assert!(Export::NoUnderscore.exported("run", ""));
        assert!(!Export::NoUnderscore.exported("_run", ""));
        assert!(Export::Requires(&["public"]).exported("f", "public static int "));
        assert!(!Export::Requires(&["public"]).exported("f", "private int "));
        assert!(!Export::Unless(&["static"]).exported("f", "static int "));
        assert!(Export::Unless(&["static"]).exported("f", "int *"));
        // whole words only: `publicity` is not `public`
        assert!(!Export::Requires(&["public"]).exported("f", "publicity "));
    }
}
