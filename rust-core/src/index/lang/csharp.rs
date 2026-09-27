//! C# pack.

use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-csharp", tree_sitter_c_sharp::LANGUAGE);

pub(super) static PACK: Pack = Pack {
    lang: Lang::CSharp,
    name: "csharp",
    extensions: &["cs"],
    grammar_id: "c-sharp-0.23",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/csharp.scm"),
    ident_kinds: &["identifier"],
    export: Export::Requires(&["public", "protected"]),
    lexical: &[
        (
            r"^\s*(?:(?:public|private|protected|internal|static|sealed|abstract|partial|readonly)\s+)*(?:class|interface|struct|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)\b",
            "Class",
        ),
        (
            r"^\s*(?:(?:public|private|protected|internal|static|virtual|override|abstract|async|sealed)\s+)+[A-Za-z_][A-Za-z0-9_<>\[\], ?.]*\s+([A-Za-z_][A-Za-z0-9_]*)\s*\([^;]*$",
            "Method",
        ),
    ],
};
