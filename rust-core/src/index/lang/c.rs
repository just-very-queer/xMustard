//! C pack.

use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-c", tree_sitter_c::LANGUAGE);

pub(super) const C_IDENTS: &[&str] = &["identifier", "type_identifier", "field_identifier"];

/// Shared by the C and C++ packs.
pub(super) const C_LEXICAL: &[(&str, &str)] = &[
    (
        r"^\s*(?:typedef\s+)?(?:struct|union|enum|class)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?::[^{]*)?\{",
        "Struct",
    ),
    (
        r"^[A-Za-z_][A-Za-z0-9_\s\*&:<>,]*[\s\*&]([A-Za-z_][A-Za-z0-9_:]*)\s*\([^;]*\)\s*(?:const\s*)?\{?\s*$",
        "Function",
    ),
];

pub(super) static PACK: Pack = Pack {
    lang: Lang::C,
    name: "c",
    extensions: &["c", "h"],
    grammar_id: "c-0.24",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/c.scm"),
    ident_kinds: C_IDENTS,
    export: Export::Unless(&["static"]),
    lexical: C_LEXICAL,
};
