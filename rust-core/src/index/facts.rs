//! Per-file facts: everything extraction learns from one file's bytes.
//!
//! Facts are path-independent (UIDs and path tokens are added when rows are written), so
//! they are cached by content: `content_key` = Git blob id of the bytes, the language and
//! the extraction mode (tree-sitter or lexical), in a compact binary encoding.
//! They hold names, kinds, ranges and hashes only. Chunk text and chunk postings are not
//! facts; they are derived from the bytes when rows are written, so the cache never holds
//! source text in any retention mode.

/// Reference kinds (stored as integers in `refs.kind`).
pub mod ref_kind {
    pub const READ: u8 = 0;
    pub const CALL: u8 = 1;
    pub const TYPE: u8 = 2;
    pub const WRITE: u8 = 3;
    pub const IMPORT: u8 = 4;
    pub const EXTENDS: u8 = 5;
    pub const IMPLEMENTS: u8 = 6;
    /// A binding site: a local, parameter or pattern name being declared.
    pub const BIND: u8 = 7;
    /// A property or field name read through a qualifier (`a.b`, `a::b`).
    pub const MEMBER: u8 = 8;
    /// A property or field name called through a qualifier (`a.b()`).
    pub const MEMBER_CALL: u8 = 9;
    /// A name listed in a local export clause (`export { a }`).
    pub const EXPORT: u8 = 10;
    /// A word from the lexical scanner (languages without a grammar); comments and
    /// string literals are stripped before scanning.
    pub const WORD: u8 = 11;
}

/// Control/data-flow context of a reference (legacy `returns`/`branches`/`writes`).
pub mod flow {
    pub const NONE: u8 = 0;
    pub const RETURNS: u8 = 1;
    pub const BRANCHES: u8 = 2;
    pub const WRITES: u8 = 3;
}

/// `files.flags` bits.
pub mod file_flag {
    pub const INVALID_UTF8: i64 = 1;
    pub const SYMBOLS_TRUNCATED: i64 = 2;
    pub const PARSE_ERRORS: i64 = 4;
    /// The file's symbols were dropped: the index reached `max_symbols`.
    pub const SYMBOL_BUDGET: i64 = 8;
    /// A grammar language extracted lexically (file above `max_parse_bytes`, or the
    /// parse failed).
    pub const LEXICAL_FALLBACK: i64 = 16;
    /// Declarations nested deeper than the extractor's qualified-name bound: their
    /// container path is shortened to a hash (`~<hash>.name`).
    pub const NESTING_TRUNCATED: i64 = 32;
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SymbolFact {
    pub name: String,
    /// Dotted container path plus name, e.g. `Outer.Inner.method`.
    pub qualified_name: String,
    /// UID label: Function, Method, Constructor, Class, Struct, Enum, Interface, Trait,
    /// TypeAlias, Union, Namespace, Module, Impl, Property, Const, Variable, EnumMember,
    /// Macro.
    pub kind: String,
    /// Index of the lexical container symbol in this file's list.
    pub container: Option<u32>,
    /// Parameter count, for callables only.
    pub arity: Option<u32>,
    pub start_byte: u32,
    pub end_byte: u32,
    /// 1-based, inclusive; `start_line` includes leading doc comments and attributes.
    pub start_line: u32,
    pub end_line: u32,
    pub name_line: u32,
    /// 0-based byte column of the name on `name_line`.
    pub name_col: u32,
    /// Hash of the normalized parameter/return (or impl header) text; empty when none.
    pub signature_hash: String,
    pub exported: bool,
    pub depth: u32,
    /// Declared inside a callable body (not visible outside it).
    pub local: bool,
    /// UID collision suffix assigned within the file (see `uid`).
    pub uid_suffix: String,
}

impl SymbolFact {
    pub fn is_callable(&self) -> bool {
        matches!(self.kind.as_str(), "Function" | "Method" | "Constructor")
    }
}

/// One reference occurrence: (name index into `FileFacts::names`, 1-based line,
/// 0-based byte column, byte offset, containing symbol index or -1, kind, flow).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct RefFact(pub u32, pub u32, pub u32, pub u32, pub i32, pub u8, pub u8);

impl RefFact {
    pub fn name_idx(&self) -> usize {
        self.0 as usize
    }
    pub fn line(&self) -> u32 {
        self.1
    }
    pub fn col(&self) -> u32 {
        self.2
    }
    pub fn start_byte(&self) -> u32 {
        self.3
    }
    pub fn symbol(&self) -> Option<u32> {
        (self.4 >= 0).then_some(self.4 as u32)
    }
    pub fn kind(&self) -> u8 {
        self.5
    }
    pub fn flow(&self) -> u8 {
        self.6
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ImportFact {
    pub line: u32,
    /// import | require | dynamic | use | mod | reexport | reexport_all | extern_crate |
    /// include
    pub kind: String,
    /// Module specifier as written: `./mod`, `net/http`, `std::collections`.
    pub module: String,
    pub name: Option<String>,
    pub alias: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChunkFact {
    /// Symbol index the chunk belongs to, if any.
    pub symbol: Option<u32>,
    /// `symbol` (a whole callable), `window` (part of a long callable) or `gap` (code
    /// between callables: headers, fields, top-level statements).
    pub kind: String,
    pub start_line: u32,
    pub end_line: u32,
    pub start_byte: u32,
    pub end_byte: u32,
    /// SHA-256 prefix of the chunk bytes (change detection for later embedding passes).
    pub content_hash: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct FileFacts {
    /// `tree_sitter` | `regex` | `none`
    pub engine: String,
    pub parse_errors: bool,
    pub invalid_utf8: bool,
    pub symbols_truncated: bool,
    pub lexical_fallback: bool,
    /// Some qualified names were shortened (see `file_flag::NESTING_TRUNCATED`).
    pub nesting_truncated: bool,
    pub line_count: u32,
    /// Distinct reference names; `RefFact.0` indexes this table.
    pub names: Vec<String>,
    pub symbols: Vec<SymbolFact>,
    pub refs: Vec<RefFact>,
    pub imports: Vec<ImportFact>,
    pub chunks: Vec<ChunkFact>,
}

impl FileFacts {
    /// Compact binary form for the fact cache (varints, delta-coded reference offsets);
    /// about a quarter of the JSON size.
    pub fn encode(&self) -> Vec<u8> {
        let mut w = Enc(Vec::with_capacity(
            64 + self.refs.len() * 8 + self.symbols.len() * 48,
        ));
        w.0.push(CODEC_VERSION);
        w.str(&self.engine);
        w.0.push(
            u8::from(self.parse_errors)
                | u8::from(self.invalid_utf8) << 1
                | u8::from(self.symbols_truncated) << 2
                | u8::from(self.lexical_fallback) << 3
                | u8::from(self.nesting_truncated) << 4,
        );
        w.uv(self.line_count as u64);
        w.uv(self.names.len() as u64);
        for n in &self.names {
            w.str(n);
        }
        w.uv(self.symbols.len() as u64);
        for s in &self.symbols {
            w.str(&s.name);
            w.str(&s.qualified_name);
            w.str(&s.kind);
            w.opt(s.container);
            w.opt(s.arity);
            for v in [
                s.start_byte,
                s.end_byte,
                s.start_line,
                s.end_line,
                s.name_line,
                s.name_col,
                s.depth,
            ] {
                w.uv(v as u64);
            }
            w.str(&s.signature_hash);
            w.0.push(u8::from(s.exported) | u8::from(s.local) << 1);
            w.str(&s.uid_suffix);
        }
        w.uv(self.refs.len() as u64);
        let (mut line, mut byte) = (0i64, 0i64);
        for r in &self.refs {
            w.uv(r.0 as u64);
            w.iv(r.1 as i64 - line);
            w.uv(r.2 as u64);
            w.iv(r.3 as i64 - byte);
            w.uv((r.4 + 1) as u64);
            w.0.push(r.5);
            w.0.push(r.6);
            line = r.1 as i64;
            byte = r.3 as i64;
        }
        w.uv(self.imports.len() as u64);
        for i in &self.imports {
            w.uv(i.line as u64);
            w.str(&i.kind);
            w.str(&i.module);
            w.opt_str(i.name.as_deref());
            w.opt_str(i.alias.as_deref());
        }
        w.uv(self.chunks.len() as u64);
        for c in &self.chunks {
            w.opt(c.symbol);
            w.str(&c.kind);
            for v in [c.start_line, c.end_line, c.start_byte, c.end_byte] {
                w.uv(v as u64);
            }
            w.str(&c.content_hash);
        }
        w.0
    }

    /// Inverse of `encode`; None for anything malformed or from another codec version.
    pub fn decode(bytes: &[u8]) -> Option<FileFacts> {
        let mut r = Dec { b: bytes, i: 0 };
        if r.byte()? != CODEC_VERSION {
            return None;
        }
        let engine = r.str()?;
        let fl = r.byte()?;
        let line_count = r.u32()?;
        let names = (0..r.len()?).map(|_| r.str()).collect::<Option<Vec<_>>>()?;
        let mut symbols = Vec::new();
        for _ in 0..r.len()? {
            let name = r.str()?;
            let qualified_name = r.str()?;
            let kind = r.str()?;
            let container = r.opt()?;
            let arity = r.opt()?;
            let v: Vec<u32> = (0..7).map(|_| r.u32()).collect::<Option<_>>()?;
            let signature_hash = r.str()?;
            let f = r.byte()?;
            let uid_suffix = r.str()?;
            symbols.push(SymbolFact {
                name,
                qualified_name,
                kind,
                container,
                arity,
                start_byte: v[0],
                end_byte: v[1],
                start_line: v[2],
                end_line: v[3],
                name_line: v[4],
                name_col: v[5],
                depth: v[6],
                signature_hash,
                exported: f & 1 != 0,
                local: f & 2 != 0,
                uid_suffix,
            });
        }
        let n_refs = r.len()?;
        let mut refs = Vec::with_capacity(n_refs.min(bytes.len()));
        let (mut line, mut byte) = (0i64, 0i64);
        for _ in 0..n_refs {
            let name = r.u32()?;
            line += r.iv()?;
            let col = r.u32()?;
            byte += r.iv()?;
            let sym = r.uv()? as i64 - 1;
            let kind = r.byte()?;
            let flow = r.byte()?;
            if (name as usize) >= names.len() || line < 0 || byte < 0 {
                return None;
            }
            refs.push(RefFact(
                name,
                line as u32,
                col,
                byte as u32,
                sym as i32,
                kind,
                flow,
            ));
        }
        let mut imports = Vec::new();
        for _ in 0..r.len()? {
            imports.push(ImportFact {
                line: r.u32()?,
                kind: r.str()?,
                module: r.str()?,
                name: r.opt_str()?,
                alias: r.opt_str()?,
            });
        }
        let mut chunks = Vec::new();
        for _ in 0..r.len()? {
            let symbol = r.opt()?;
            let kind = r.str()?;
            let v: Vec<u32> = (0..4).map(|_| r.u32()).collect::<Option<_>>()?;
            chunks.push(ChunkFact {
                symbol,
                kind,
                start_line: v[0],
                end_line: v[1],
                start_byte: v[2],
                end_byte: v[3],
                content_hash: r.str()?,
            });
        }
        if r.i != bytes.len() {
            return None;
        }
        Some(FileFacts {
            engine,
            parse_errors: fl & 1 != 0,
            invalid_utf8: fl & 2 != 0,
            symbols_truncated: fl & 4 != 0,
            lexical_fallback: fl & 8 != 0,
            nesting_truncated: fl & 16 != 0,
            line_count,
            names,
            symbols,
            refs,
            imports,
            chunks,
        })
    }

    pub fn flags(&self) -> i64 {
        let mut f = 0;
        if self.invalid_utf8 {
            f |= file_flag::INVALID_UTF8;
        }
        if self.symbols_truncated {
            f |= file_flag::SYMBOLS_TRUNCATED;
        }
        if self.parse_errors {
            f |= file_flag::PARSE_ERRORS;
        }
        if self.lexical_fallback {
            f |= file_flag::LEXICAL_FALLBACK;
        }
        if self.nesting_truncated {
            f |= file_flag::NESTING_TRUNCATED;
        }
        f
    }
}

const CODEC_VERSION: u8 = 2;

struct Enc(Vec<u8>);

impl Enc {
    fn uv(&mut self, mut v: u64) {
        loop {
            let b = (v & 0x7f) as u8;
            v >>= 7;
            if v == 0 {
                self.0.push(b);
                return;
            }
            self.0.push(b | 0x80);
        }
    }
    fn iv(&mut self, v: i64) {
        self.uv(((v << 1) ^ (v >> 63)) as u64);
    }
    fn str(&mut self, s: &str) {
        self.uv(s.len() as u64);
        self.0.extend_from_slice(s.as_bytes());
    }
    fn opt(&mut self, v: Option<u32>) {
        self.uv(v.map_or(0, |x| x as u64 + 1));
    }
    fn opt_str(&mut self, v: Option<&str>) {
        match v {
            Some(s) => {
                self.0.push(1);
                self.str(s);
            }
            None => self.0.push(0),
        }
    }
}

struct Dec<'a> {
    b: &'a [u8],
    i: usize,
}

impl Dec<'_> {
    fn byte(&mut self) -> Option<u8> {
        let v = *self.b.get(self.i)?;
        self.i += 1;
        Some(v)
    }
    fn uv(&mut self) -> Option<u64> {
        let mut v = 0u64;
        for shift in (0..64).step_by(7) {
            let b = self.byte()?;
            v |= u64::from(b & 0x7f) << shift;
            if b & 0x80 == 0 {
                return Some(v);
            }
        }
        None
    }
    fn iv(&mut self) -> Option<i64> {
        let u = self.uv()?;
        Some((u >> 1) as i64 ^ -((u & 1) as i64))
    }
    fn u32(&mut self) -> Option<u32> {
        u32::try_from(self.uv()?).ok()
    }
    fn len(&mut self) -> Option<usize> {
        let n = usize::try_from(self.uv()?).ok()?;
        // every element takes at least one byte: reject impossible counts early.
        (n <= self.b.len() - self.i).then_some(n)
    }
    fn str(&mut self) -> Option<String> {
        let n = self.len()?;
        let s = std::str::from_utf8(self.b.get(self.i..self.i + n)?)
            .ok()?
            .to_string();
        self.i += n;
        Some(s)
    }
    fn opt(&mut self) -> Option<Option<u32>> {
        let v = self.uv()?;
        Some(if v == 0 {
            None
        } else {
            Some(u32::try_from(v - 1).ok()?)
        })
    }
    fn opt_str(&mut self) -> Option<Option<String>> {
        match self.byte()? {
            0 => Some(None),
            1 => Some(Some(self.str()?)),
            _ => None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn facts_round_trip() {
        let f = FileFacts {
            engine: "tree_sitter".into(),
            line_count: 3,
            names: vec!["foo".into()],
            symbols: vec![SymbolFact {
                name: "a".into(),
                qualified_name: "A.a".into(),
                kind: "Method".into(),
                container: Some(0),
                arity: Some(2),
                start_byte: 1,
                end_byte: 9,
                start_line: 1,
                end_line: 2,
                name_line: 1,
                name_col: 3,
                signature_hash: "ab".into(),
                exported: true,
                depth: 1,
                local: false,
                uid_suffix: String::new(),
            }],
            refs: vec![RefFact(0, 2, 4, 20, 0, ref_kind::CALL, flow::RETURNS)],
            imports: vec![ImportFact {
                line: 1,
                kind: "import".into(),
                module: "./x".into(),
                name: Some("X".into()),
                alias: None,
            }],
            chunks: vec![],
            ..Default::default()
        };
        let bytes = f.encode();
        let back = FileFacts::decode(&bytes).unwrap();
        assert_eq!(back, f);
        assert_eq!(back.refs[0].symbol(), Some(0));
        // truncated or extended input is rejected, never misread
        assert!(FileFacts::decode(&bytes[..bytes.len() - 1]).is_none());
        let mut longer = bytes.clone();
        longer.push(0);
        assert!(FileFacts::decode(&longer).is_none());
    }
}
