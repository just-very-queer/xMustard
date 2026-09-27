//! Python pack.

use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-python", tree_sitter_python::LANGUAGE);

pub(super) static PACK: Pack = Pack {
    lang: Lang::Python,
    name: "python",
    extensions: &["py", "pyi"],
    grammar_id: "python-0.25",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/python.scm"),
    ident_kinds: &["identifier"],
    export: Export::NoUnderscore,
    lexical: &[
        (
            r"^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(",
            "Function",
        ),
        (r"^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)\b", "Class"),
    ],
};
