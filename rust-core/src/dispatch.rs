//! Types for the `xmustard-core` subcommand table.
//!
//! The binary owns the table (`COMMANDS` in `src/bin/xmustard-core.rs`). The one-shot
//! CLI and the resident `serve` worker both dispatch through it, so adding a
//! subcommand means adding one table entry and its handler. A handler returns its
//! output or error as a value. It never prints and never exits the process, so it can
//! run inside a long-lived worker.

use std::fmt::Display;

use serde::Serialize;

/// A subcommand's positional arguments: everything after the subcommand name.
pub type Args = std::vec::IntoIter<String>;

/// What a subcommand produced on success.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Output {
    /// One JSON document. The CLI prints it followed by a newline. `serve` embeds it
    /// verbatim as the JSON-RPC `result`.
    Json(String),
    /// Verbatim text, such as a Markdown projection, printed without a trailing
    /// newline. `serve` refuses it, so text-producing subcommands are one-shot only.
    Text(String),
}

/// A failed subcommand. The CLI prints `message` to stderr and exits with `code`:
/// 2 is usage, 1 is failure, 3 is a goal guardrail refusal and 4 is not found.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CmdError {
    pub code: i32,
    pub message: String,
}

impl CmdError {
    pub fn new(code: i32, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
        }
    }

    /// A usage error: `usage: <usage>`, exit code 2.
    pub fn usage(usage: impl Display) -> Self {
        Self::new(2, format!("usage: {usage}"))
    }

    /// A runtime failure, exit code 1.
    pub fn failed(message: impl Into<String>) -> Self {
        Self::new(1, message)
    }
}

pub type CmdResult = Result<Output, CmdError>;

/// Whether a subcommand may run inside the resident `serve` worker.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Residency {
    /// One-shot only. Such a command spawns long-lived or arbitrary children
    /// (language servers, verification commands, ast-grep), writes durable goal state,
    /// or prints text. The per-call process keeps its kill boundary.
    OneShot,
    /// Safe in-process: returns JSON and spawns only short, bounded git children.
    Resident,
    /// Resident, except when the first argument (a sub-subcommand) is listed.
    ResidentExcept(&'static [&'static str]),
}

/// One subcommand table entry.
pub struct Command {
    pub name: &'static str,
    pub residency: Residency,
    pub run: fn(Args) -> CmdResult,
}

impl Command {
    /// Whether this invocation (with its arguments) may run inside `serve`.
    pub fn resident_for(&self, args: &[String]) -> bool {
        match self.residency {
            Residency::OneShot => false,
            Residency::Resident => true,
            Residency::ResidentExcept(excluded) => args
                .first()
                .is_none_or(|sub| !excluded.contains(&sub.as_str())),
        }
    }

    /// Whether any invocation of this command may run inside `serve`.
    pub fn is_resident(&self) -> bool {
        !matches!(self.residency, Residency::OneShot)
    }
}

/// The table entry named `name`.
pub fn find<'a>(table: &'a [Command], name: &str) -> Option<&'a Command> {
    table.iter().find(|c| c.name == name)
}

/// `value` as the single-line JSON document the CLI prints.
pub fn json<T: Serialize + ?Sized>(value: &T) -> CmdResult {
    Ok(Output::Json(
        serde_json::to_string(value).expect("command result should serialize"),
    ))
}

/// The next positional argument, or a usage error naming `usage`.
pub fn need(args: &mut Args, usage: &str) -> Result<String, CmdError> {
    args.next().ok_or_else(|| CmdError::usage(usage))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ok(_: Args) -> CmdResult {
        json(&serde_json::json!({"ok": true}))
    }

    const TABLE: &[Command] = &[
        Command {
            name: "a",
            residency: Residency::Resident,
            run: ok,
        },
        Command {
            name: "b",
            residency: Residency::OneShot,
            run: ok,
        },
        Command {
            name: "c",
            residency: Residency::ResidentExcept(&["spawn"]),
            run: ok,
        },
    ];

    #[test]
    fn residency_is_per_invocation() {
        let args = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert!(find(TABLE, "a").unwrap().resident_for(&args(&["x"])));
        assert!(!find(TABLE, "b").unwrap().resident_for(&args(&["x"])));
        let c = find(TABLE, "c").unwrap();
        assert!(c.is_resident());
        assert!(c.resident_for(&args(&["read"])));
        assert!(c.resident_for(&[]));
        assert!(!c.resident_for(&args(&["spawn", "x"])));
        assert!(find(TABLE, "missing").is_none());
    }

    #[test]
    fn need_reports_usage_with_exit_code_two() {
        let mut args: Args = vec!["one".to_string()].into_iter();
        assert_eq!(need(&mut args, "u <x> <y>").unwrap(), "one");
        let err = need(&mut args, "u <x> <y>").unwrap_err();
        assert_eq!(err.code, 2);
        assert_eq!(err.message, "usage: u <x> <y>");
    }

    #[test]
    fn json_output_is_one_line() {
        let Output::Json(s) = json(&serde_json::json!({"a": "x\ny"})).unwrap() else {
            panic!("json() must produce Output::Json");
        };
        assert!(!s.contains('\n'));
    }
}
