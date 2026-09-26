//! Stable symbol UIDs (PAR-SYM-01).
//!
//! Form: `Label:path:qualifiedName` plus `#arity` for callables, e.g.
//! `Method:src/widget.ts:Widget.render#1`. Line numbers never enter a UID, so edits
//! elsewhere in the file leave it unchanged. Only when two symbols of one file share that
//! base is a suffix added, to every member of the colliding group:
//! `~<8 hex of the signature hash>` (a type-hash: distinct parameter types separate
//! overloads), then `~<n>` in source order if signatures are identical too.

use std::collections::HashMap;

use super::facts::SymbolFact;

/// The base UID without the collision suffix.
pub fn base_uid(kind: &str, path: &str, qualified_name: &str, arity: Option<u32>) -> String {
    match arity {
        Some(a) => format!("{kind}:{path}:{qualified_name}#{a}"),
        None => format!("{kind}:{path}:{qualified_name}"),
    }
}

pub fn uid(path: &str, s: &SymbolFact) -> String {
    let mut u = base_uid(&s.kind, path, &s.qualified_name, s.arity);
    u.push_str(&s.uid_suffix);
    u
}

/// Assign `uid_suffix` for symbols whose path-independent base collides in this file.
pub fn assign_suffixes(symbols: &mut [SymbolFact]) {
    let key = |s: &SymbolFact| (s.kind.clone(), s.qualified_name.clone(), s.arity);
    let mut groups: HashMap<(String, String, Option<u32>), Vec<usize>> = HashMap::new();
    for (i, s) in symbols.iter().enumerate() {
        groups.entry(key(s)).or_default().push(i);
    }
    for members in groups.into_values().filter(|m| m.len() > 1) {
        let mut by_sig: HashMap<String, Vec<usize>> = HashMap::new();
        for &i in &members {
            let short: String = symbols[i].signature_hash.chars().take(8).collect();
            let short = if short.is_empty() {
                "00000000".to_string()
            } else {
                short
            };
            by_sig.entry(short).or_default().push(i);
        }
        for (sig, mut same) in by_sig {
            same.sort_unstable();
            if same.len() == 1 {
                symbols[same[0]].uid_suffix = format!("~{sig}");
            } else {
                for (n, i) in same.into_iter().enumerate() {
                    symbols[i].uid_suffix = format!("~{sig}~{}", n + 1);
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sym(kind: &str, q: &str, arity: Option<u32>, sig: &str) -> SymbolFact {
        SymbolFact {
            name: q.rsplit('.').next().unwrap().to_string(),
            qualified_name: q.to_string(),
            kind: kind.to_string(),
            container: None,
            arity,
            start_byte: 0,
            end_byte: 0,
            start_line: 1,
            end_line: 1,
            name_line: 1,
            name_col: 0,
            signature_hash: sig.to_string(),
            exported: false,
            depth: 0,
            local: false,
            uid_suffix: String::new(),
        }
    }

    #[test]
    fn suffixes_only_on_collision() {
        let mut v = vec![
            sym("Function", "over", Some(1), "aaaaaaaa11"),
            sym("Function", "over", Some(1), "bbbbbbbb22"),
            sym("Function", "over", Some(2), "cccccccc33"),
            sym("Method", "A.m", Some(0), "dddddddd44"),
            sym("Method", "A.m", Some(0), "dddddddd44"),
        ];
        assign_suffixes(&mut v);
        assert_eq!(uid("f.ts", &v[0]), "Function:f.ts:over#1~aaaaaaaa");
        assert_eq!(uid("f.ts", &v[1]), "Function:f.ts:over#1~bbbbbbbb");
        assert_eq!(uid("f.ts", &v[2]), "Function:f.ts:over#2");
        assert_eq!(uid("f.ts", &v[3]), "Method:f.ts:A.m#0~dddddddd~1");
        assert_eq!(uid("f.ts", &v[4]), "Method:f.ts:A.m#0~dddddddd~2");
    }

    #[test]
    fn non_callables_have_no_arity() {
        let s = sym("Class", "Outer.Inner", None, "");
        assert_eq!(uid("a.ts", &s), "Class:a.ts:Outer.Inner");
    }
}
