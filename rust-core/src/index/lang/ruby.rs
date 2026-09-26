//! Ruby pack.

use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-ruby", tree_sitter_ruby::LANGUAGE);

pub(super) static PACK: Pack = Pack {
    lang: Lang::Ruby,
    name: "ruby",
    extensions: &["rb", "rake"],
    grammar_id: "ruby-0.23",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/ruby.scm"),
    ident_kinds: &["identifier", "constant"],
    // visibility is a runtime call (`private`), not part of the declaration
    export: Export::Unless(&[]),
    lexical: &[
        (
            r"^\s*def\s+(?:self\.)?([A-Za-z_][A-Za-z0-9_]*[?!]?)",
            "Function",
        ),
        (r"^\s*class\s+([A-Z][A-Za-z0-9_]*)\b", "Class"),
        (r"^\s*module\s+([A-Z][A-Za-z0-9_]*)\b", "Module"),
    ],
};
