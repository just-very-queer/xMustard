use crate::shapes::{Circle, Shape, Square};
use crate::util;
use crate::util::helper as h;

pub fn run() -> f64 {
    let mut c = Circle::new(1.0);
    c.grow();
    let a = c.area();
    let d = c.describe();
    let sq = Square { s: 2.0 };
    let b = sq.area();
    util::twice(a) + h(b) + crate::util::helper(d.len() as f64)
}
