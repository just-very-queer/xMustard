//! Swift pack.

use super::{Export, Extractor, Lang, Pack, gated_grammar};

gated_grammar!(GRAMMAR, "lang-swift", tree_sitter_swift::LANGUAGE);

pub(super) static PACK: Pack = Pack {
    lang: Lang::Swift,
    name: "swift",
    extensions: &["swift"],
    grammar_id: "swift-0.7",
    grammar: GRAMMAR,
    extractor: Extractor::Tags,
    tags: include_str!("queries/swift.scm"),
    ident_kinds: &["simple_identifier", "type_identifier"],
    export: Export::Unless(&["private", "fileprivate"]),
    lexical: &[
        (
            r"^\s*(?:(?:public|private|fileprivate|internal|open|static|class|override|mutating|final)\s+)*func\s+([A-Za-z_][A-Za-z0-9_]*)",
            "Function",
        ),
        (
            r"^\s*(?:(?:public|private|fileprivate|internal|open|final)\s+)*(?:class|struct|enum|protocol|actor)\s+([A-Za-z_][A-Za-z0-9_]*)\b",
            "Class",
        ),
    ],
};
