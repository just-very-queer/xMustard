//! Java pack.

use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-java", tree_sitter_java::LANGUAGE);

pub(super) static PACK: Pack = Pack {
    lang: Lang::Java,
    name: "java",
    extensions: &["java"],
    grammar_id: "java-0.23",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/java.scm"),
    ident_kinds: &["identifier", "type_identifier"],
    export: Export::Requires(&["public", "protected"]),
    lexical: &[
        (
            r"^\s*(?:(?:public|private|protected|static|final|abstract|sealed)\s+)*(?:class|interface|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)\b",
            "Class",
        ),
        (
            r"^\s*(?:(?:public|private|protected|static|final|abstract|synchronized|native|default)\s+)+[A-Za-z_][A-Za-z0-9_<>\[\], ?]*\s+([A-Za-z_][A-Za-z0-9_]*)\s*\([^;]*$",
            "Method",
        ),
    ],
};
