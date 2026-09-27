//! C++ pack.

use super::c::{C_IDENTS, C_LEXICAL};
use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-cpp", tree_sitter_cpp::LANGUAGE);

pub(super) static PACK: Pack = Pack {
    lang: Lang::Cpp,
    name: "cpp",
    extensions: &["cpp", "hpp", "cc", "cxx", "hh", "hxx"],
    grammar_id: "cpp-0.23",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/cpp.scm"),
    ident_kinds: C_IDENTS,
    export: Export::Unless(&["static"]),
    lexical: C_LEXICAL,
};
