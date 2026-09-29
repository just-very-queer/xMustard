//! Method resolution order: the sequence of types a member is searched in.
//!
//! `FirstWins` is a depth-first walk in declaration order (TS/JS, Java's class chain
//! before its interfaces, Rust's own impls before the traits it implements). `C3` is
//! Python's linearization: every type precedes its bases and the declared base order
//! is kept; a hierarchy with no such order is inconsistent, and `linearize` then
//! reports it and falls back to the depth-first order.

use std::collections::{HashMap, HashSet};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Order {
    FirstWins,
    C3,
}

/// The search order for `root` over `parents` (type id → its declared bases in order),
/// `root` first, and whether the order is consistent (always true for `FirstWins`).
pub fn linearize(root: i64, order: Order, parents: &HashMap<i64, Vec<i64>>) -> (Vec<i64>, bool) {
    let c3 = match order {
        Order::C3 => c3(root, parents, &mut HashSet::new()),
        Order::FirstWins => None,
    };
    match c3 {
        Some(l) => (l, true),
        None => (first_wins(root, parents), order == Order::FirstWins),
    }
}

fn first_wins(root: i64, parents: &HashMap<i64, Vec<i64>>) -> Vec<i64> {
    let mut out = Vec::new();
    let mut stack = vec![root];
    while let Some(t) = stack.pop() {
        if out.contains(&t) {
            continue;
        }
        out.push(t);
        stack.extend(parents.get(&t).into_iter().flatten().rev());
    }
    out
}

/// `t` followed by the merge of its bases' linearizations and the base list; None when
/// no consistent order exists (or a base cycle is met).
fn c3(t: i64, parents: &HashMap<i64, Vec<i64>>, path: &mut HashSet<i64>) -> Option<Vec<i64>> {
    if !path.insert(t) {
        return None;
    }
    let bases = parents.get(&t).map(Vec::as_slice).unwrap_or_default();
    let mut seqs: Vec<Vec<i64>> = bases
        .iter()
        .map(|b| c3(*b, parents, path))
        .collect::<Option<_>>()?;
    path.remove(&t);
    seqs.push(bases.to_vec());
    let mut out = vec![t];
    loop {
        seqs.retain(|s| !s.is_empty());
        if seqs.is_empty() {
            return Some(out);
        }
        // the first head that appears in no other sequence's tail
        let next = seqs
            .iter()
            .map(|s| s[0])
            .find(|h| seqs.iter().all(|s| !s[1..].contains(h)))?;
        out.push(next);
        for s in &mut seqs {
            if s[0] == next {
                s.remove(0);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn h(edges: &[(i64, &[i64])]) -> HashMap<i64, Vec<i64>> {
        edges.iter().map(|(k, v)| (*k, v.to_vec())).collect()
    }

    #[test]
    fn c3_diamond_puts_shared_base_last() {
        // D(B, C), B(A), C(A)
        let p = h(&[(4, &[2, 3]), (2, &[1]), (3, &[1])]);
        assert_eq!(linearize(4, Order::C3, &p), (vec![4, 2, 3, 1], true));
        // depth-first order visits A before C
        assert_eq!(linearize(4, Order::FirstWins, &p), (vec![4, 2, 1, 3], true));
    }

    #[test]
    fn c3_keeps_declared_base_order() {
        // the classic O / X, Y, Z example: K1(A,B,C), K2(D,B,E), K3(D,A), Z(K1,K2,K3)
        let (o, a, b, c, d, e) = (0, 1, 2, 3, 4, 5);
        let (k1, k2, k3, z) = (11, 12, 13, 20);
        let p = h(&[
            (a, &[o]),
            (b, &[o]),
            (c, &[o]),
            (d, &[o]),
            (e, &[o]),
            (k1, &[a, b, c]),
            (k2, &[d, b, e]),
            (k3, &[d, a]),
            (z, &[k1, k2, k3]),
        ]);
        assert_eq!(
            linearize(z, Order::C3, &p),
            (vec![z, k1, k2, k3, d, a, b, c, e, o], true)
        );
    }

    #[test]
    fn inconsistent_hierarchy_falls_back_and_is_reported() {
        // X(A, B), Y(B, A), Z(X, Y): no consistent order
        let p = h(&[(10, &[1, 2]), (11, &[2, 1]), (12, &[10, 11])]);
        let (order, consistent) = linearize(12, Order::C3, &p);
        assert!(!consistent);
        assert_eq!(order[0], 12);
        assert_eq!(order.len(), 5);
    }

    #[test]
    fn cycles_terminate() {
        let p = h(&[(1, &[2]), (2, &[1])]);
        assert_eq!(linearize(1, Order::FirstWins, &p).0, vec![1, 2]);
        assert!(!linearize(1, Order::C3, &p).1);
    }
}
