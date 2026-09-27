//! Bash pack.

use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-bash", tree_sitter_bash::LANGUAGE);

pub(super) static PACK: Pack = Pack {
    lang: Lang::Bash,
    name: "bash",
    extensions: &["sh", "bash"],
    grammar_id: "bash-0.25",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/bash.scm"),
    ident_kinds: &["variable_name"],
    export: Export::Unless(&[]),
    lexical: &[
        (r"^\s*function\s+([A-Za-z_][A-Za-z0-9_:.-]*)", "Function"),
        (r"^\s*([A-Za-z_][A-Za-z0-9_:.-]*)\s*\(\s*\)", "Function"),
    ],
};
