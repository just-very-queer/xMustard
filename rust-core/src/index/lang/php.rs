//! PHP pack.

use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-php", tree_sitter_php::LANGUAGE_PHP);

pub(super) static PACK: Pack = Pack {
    lang: Lang::Php,
    name: "php",
    extensions: &["php"],
    grammar_id: "php-0.24",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/php.scm"),
    ident_kinds: &["name"],
    export: Export::Unless(&["private"]),
    lexical: &[
        (
            r"^\s*(?:(?:public|private|protected|static|final|abstract)\s+)*function\s+&?([A-Za-z_][A-Za-z0-9_]*)\s*\(",
            "Function",
        ),
        (
            r"^\s*(?:(?:final|abstract|readonly)\s+)*(?:class|interface|trait|enum)\s+([A-Za-z_][A-Za-z0-9_]*)\b",
            "Class",
        ),
    ],
};
