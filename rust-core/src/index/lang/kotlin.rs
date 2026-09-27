//! Kotlin pack (tree-sitter-kotlin-ng).

use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-kotlin", tree_sitter_kotlin_ng::LANGUAGE);

pub(super) static PACK: Pack = Pack {
    lang: Lang::Kotlin,
    name: "kotlin",
    extensions: &["kt", "kts"],
    grammar_id: "kotlin-ng-1.1",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/kotlin.scm"),
    ident_kinds: &["identifier"],
    export: Export::Unless(&["private", "internal"]),
    lexical: &[
        (
            r"^\s*(?:(?:public|private|protected|internal|override|open|suspend|inline|operator|infix|tailrec|abstract)\s+)*fun\s+(?:<[^>]*>\s*)?(?:[A-Za-z_][A-Za-z0-9_.]*\.)?([A-Za-z_][A-Za-z0-9_]*)\s*\(",
            "Function",
        ),
        (
            r"^\s*(?:(?:public|private|protected|internal|open|abstract|sealed|data|enum|annotation|inner|value)\s+)*(?:class|interface|object)\s+([A-Za-z_][A-Za-z0-9_]*)\b",
            "Class",
        ),
    ],
};
