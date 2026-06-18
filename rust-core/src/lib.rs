//! xMustard Rust core: systems-safe ownership of scanning, repo maps,
//! verification, diagnostics, and the durable goal/swarm runtime.
//!
//! Memory safety is compiler-enforced for the whole crate.
#![forbid(unsafe_code)]

pub mod benchmark;
pub mod changetrack;
pub mod models;
pub mod contracts;
pub mod diagnostics;
pub mod goalruntime;
pub mod lsp;
pub mod repomap;
pub mod scanner;
pub mod search;
pub mod semantic;
pub mod symbolgraph;
pub mod swarm;
pub mod verification;
pub mod wiki;
