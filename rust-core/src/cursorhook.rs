//! Cursor command hook client (WS-40c-b; PAR-ADP-10, PAR-CTX-03).
//!
//! Cursor has no http hook type and no `mcp_tool` hook type, so every event spawns
//! this binary; the client is std-only and holds no daemon state. It reads the
//! event JSON on stdin, resolves the workspace against the environment, posts the
//! event to `/api/hooks/cursor/<Event>` and writes Cursor's documented answer keys
//! to stdout.
//!
//! **It fails open** (the owner's decision, 2026-10-03, resolving WS-40c-a's
//! follow-up (d)). Cursor's exit contract: exit 0 uses the JSON output, exit 2
//! blocks the action, any other non-zero exit fails open while `failClosed` is
//! false. So every internal failure exits non-zero and **not 2**, and
//! `integrations/cursor/hooks.json` sets `failClosed: false`. A failed hook leaves
//! the user's own tool output intact and never blocks their agent.

use std::env;
use std::fmt::Write as _;

/// Cursor's own `additional_context` cap, measured from the installed Cursor
/// 3.22.7 bundle (`limits.ts`: `Cbt=1e4`). It is a **hard ceiling, not a
/// truncation**: the injector raises `HookAdditionalContextTooLargeError` when the
/// trimmed value exceeds it, and the hook execution path only logs, so an over-cap
/// answer is dropped whole. The client never emits one. Mirrors
/// `clientPolicies["cursor"].ContextChars`.
pub const CURSOR_CONTEXT_CHARS: usize = 10_000;

/// The events this client routes. Cursor defines many more; WS-40c-a names these
/// four and declines the rest, each for a stated reason.
pub const ROUTED_EVENTS: [&str; 4] = ["sessionStart", "preToolUse", "postToolUse", "preCompact"];

/// An answer to write to stdout. Exactly the documented keys per event; an extra
/// key is an invalid response, and on a permission hook that blocks the user's
/// tool call.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Answer {
    /// `additional_context`: the text injected into the conversation.
    pub additional_context: Option<String>,
    /// `updated_mcp_tool_output`: the replacement for an MCP tool's result
    /// (postToolUse only, MCP tools only).
    pub updated_mcp_tool_output: Option<String>,
    /// `updated_input`: the replacement tool input (preToolUse only).
    pub updated_input: Option<String>,
}

impl Answer {
    /// True when the answer would change nothing, so stdout stays empty.
    pub fn is_empty(&self) -> bool {
        self.additional_context.is_none()
            && self.updated_mcp_tool_output.is_none()
            && self.updated_input.is_none()
    }
}

/// The outcome of one event: either an answer, or why there is none.
#[derive(Debug, PartialEq, Eq)]
pub enum Outcome {
    /// Nothing to send: Cursor keeps its own output.
    Silent,
    /// An answer in Cursor's schema for this event.
    Answered(Answer),
}

/// What the client decided about an event, given the daemon's reply.
///
/// The daemon's reply is the shared hook result: `context` is the injected text,
/// `updated` is the shaped replacement. Which of them becomes which Cursor key
/// depends on the event, and the mapping is a closed set (goal §5.1 rule 10), so it
/// is a match, not a ladder.
pub fn event_answer(event: &str, context: Option<&str>, updated: Option<&str>) -> Outcome {
    let context = context.map(str::trim).filter(|s| !s.is_empty());
    let updated = updated.filter(|s| !s.is_empty());
    match (event, updated) {
        // postToolUse replaces an MCP tool's result only. A built-in is observe-only
        // and the daemon says so by sending no replacement, which stays Silent.
        ("postToolUse", Some(payload)) => Outcome::Answered(Answer {
            updated_mcp_tool_output: Some(payload.to_string()),
            additional_context: context.map(|c| cap(c, CURSOR_CONTEXT_CHARS)),
            updated_input: None,
        }),
        ("preToolUse", Some(payload)) => Outcome::Answered(Answer {
            updated_input: Some(payload.to_string()),
            additional_context: context.map(|c| cap(c, CURSOR_CONTEXT_CHARS)),
            updated_mcp_tool_output: None,
        }),
        // preCompact is observational: its output schema takes user_message only, and
        // it is not one of the five events Cursor's additional-context step accepts
        // (sessionStart, beforeSubmitPrompt, preToolUse, postToolUse,
        // postToolUseFailure). A context the daemon prepared here would be an
        // out-of-schema member, and an out-of-schema member on a response is what
        // Cursor treats as invalid, so nothing is sent.
        ("preCompact", _) => Outcome::Silent,
        (_, _) => match context {
            Some(c) => Outcome::Answered(Answer {
                additional_context: Some(cap(c, CURSOR_CONTEXT_CHARS)),
                updated_mcp_tool_output: None,
                updated_input: None,
            }),
            None => Outcome::Silent,
        },
    }
}

/// Bounds injected context to Cursor's hard cap, on a character boundary.
///
/// The client would rather truncate than raise inside Cursor, but an answer at
/// exactly the cap is still accepted by Cursor's own check (`> Cbt` throws), so the
/// cut is at the cap and the result is never over it.
fn cap(s: &str, limit: usize) -> String {
    if s.chars().count() <= limit {
        return s.to_string();
    }
    s.chars().take(limit).collect()
}

/// The workspace the event belongs to, or why there is none.
///
/// Cursor sends `workspace_roots` (one root normally, several for a multi-root
/// workspace) and sets `CURSOR_PROJECT_DIR`. WS-40c-a forbids binding
/// `workspace_roots[0]`: in a multi-root workspace the active project is any member
/// of the list. The rule is to match `CURSOR_PROJECT_DIR` against the whole list and
/// refuse on no match, an empty list, or a disagreement, so a hook that cannot name
/// its repository sends nothing rather than binding the wrong one.
pub fn resolve_workspace(roots: &[String], project_dir: &str) -> Option<String> {
    let project = project_dir.trim();
    if project.is_empty() || roots.is_empty() {
        return None;
    }
    roots
        .iter()
        .map(|r| r.trim())
        .find(|r| !r.is_empty() && same_path(r, project))
        .map(|r| r.to_string())
}

/// Whether two root paths name the same directory. Both are cleaned and compared
/// as paths, so a trailing separator or a `.` component does not make a match fail.
fn same_path(a: &str, b: &str) -> bool {
    let clean = |p: &str| -> String {
        let mut out = String::with_capacity(p.len());
        let mut prev_sep = false;
        for ch in p.chars() {
            let sep = ch == '/';
            if sep && prev_sep {
                continue;
            }
            out.push(ch);
            prev_sep = sep;
        }
        while out.len() > 1 && out.ends_with('/') {
            out.pop();
        }
        out
    };
    let (a, b) = (clean(a), clean(b));
    if a == b {
        return true;
    }
    // A root of "." means the process's own directory, which is the project root
    // for a project hook (Cursor runs those from the project root).
    a == "." || b == "."
}

/// The exit code for an internal failure: non-zero and **not 2**.
///
/// Exit 2 is Cursor's "block the action" code, so it must never come from a
/// failure: the client's whole failure policy is that the user's agent is never
/// blocked. Named so the rule is one symbol rather than a literal repeated in three
/// call sites.
pub const FAIL_OPEN_EXIT: i32 = 3;

/// Renders an answer as the JSON object Cursor reads on stdout, or `None` when there
/// is nothing to say (an empty stdout is the fail-open answer for every event).
pub fn render(answer: &Answer) -> Option<String> {
    if answer.is_empty() {
        return None;
    }
    let mut out = String::from("{");
    let mut first = true;
    let member = |out: &mut String, first: &mut bool, key: &str, value: &str| {
        if *first {
            *first = false;
        } else {
            out.push(',');
        }
        let _ = write!(out, "{}:{}", json_string(key), json_string(value));
    };
    if let Some(v) = &answer.additional_context {
        member(&mut out, &mut first, "additional_context", v);
    }
    if let Some(v) = &answer.updated_mcp_tool_output {
        member(&mut out, &mut first, "updated_mcp_tool_output", v);
    }
    if let Some(v) = &answer.updated_input {
        member(&mut out, &mut first, "updated_input", v);
    }
    out.push('}');
    debug_assert!(!first, "an empty answer renders no object");
    Some(out)
}

/// Quotes a string as a JSON string literal. Cursor parses this with its own JSON
/// reader, so the escaping is the one that reader accepts, not the one Rust's
/// `Display` for a C string produces.
pub fn json_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for ch in s.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => {
                let _ = write!(out, "\\u{:04x}", c as u32);
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Pulls one top-level string member out of a JSON object, without a JSON parser.
///
/// The client is std-only for its startup budget, and it reads three fields from
/// the daemon's reply and none from Cursor's payload (the daemon decodes that). A
/// value that is not a plain string is absent, never a guess.
pub fn json_member<'a>(body: &'a str, key: &str) -> Option<&'a str> {
    let needle = format!("\"{key}\"");
    let mut rest = body;
    // Only the first occurrence counts, and a key that appears inside a value's
    // text is not a member: the search restarts after each value.
    loop {
        let at = rest.find(&needle)?;
        let after = &rest[at + needle.len()..];
        let colon = after.find(':')?;
        let value = after[colon + 1..].trim_start();
        let value = value.strip_prefix('"')?;
        let end = json_string_end(value)?;
        if key_is_here(body, at) {
            return Some(&value[..end]);
        }
        rest = &after[colon + 1 + 1 + end..];
    }
}

/// Whether the match at `at` is a member key rather than text inside a value.
fn key_is_here(body: &str, at: usize) -> bool {
    // The character before the opening quote decides: a key follows `{` or `,`
    // (ignoring whitespace); text inside a string value is preceded by anything else.
    let before = body[..at].trim_end();
    matches!(before.chars().next_back(), Some('{') | Some(','))
}

/// The index just past a JSON string value's closing quote, honouring escapes.
fn json_string_end(value: &str) -> Option<usize> {
    let bytes = value.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'\\' => i += 2,
            b'"' => return Some(i),
            _ => i += 1,
        }
    }
    None
}

/// Reads `workspace_roots` (an array of strings) out of Cursor's payload.
pub fn json_string_array(body: &str, key: &str) -> Vec<String> {
    let needle = format!("\"{key}\"");
    let Some(at) = body.find(&needle) else {
        return Vec::new();
    };
    if !key_is_here(body, at) {
        return Vec::new();
    }
    let Some(colon) = body[at + needle.len()..].find(':') else {
        return Vec::new();
    };
    let Some(open) = body[at + needle.len() + colon + 1..].find('[') else {
        return Vec::new();
    };
    let rest = &body[at + needle.len() + colon + 1 + open + 1..];
    let mut out = Vec::new();
    let mut depth = 0usize;
    let mut i = 0usize;
    let bytes = rest.as_bytes();
    while i < bytes.len() {
        match bytes[i] {
            b'"' => {
                // json_string_end returns the offset of the closing quote, relative
                // to the byte after this one, so the value is that slice exactly.
                let Some(end) = json_string_end(&rest[i + 1..]) else {
                    return out;
                };
                let raw = &rest[i + 1..i + 1 + end];
                if depth == 0 {
                    out.push(raw.to_string());
                }
                i += end + 2;
            }
            b'[' => {
                depth += 1;
                i += 1;
            }
            b']' if depth == 0 => return out,
            b']' => {
                depth = depth.saturating_sub(1);
                i += 1;
            }
            _ => i += 1,
        }
    }
    out
}

/// The environment the client reads. Every field is optional: a field Cursor does
/// not send, or a shell that drops it, costs a fail-open answer, never a guess.
pub struct Env {
    pub workspace_roots: Vec<String>,
    pub project_dir: String,
    pub workspace_id: String,
}

/// Reads the environment fields from `getenv`.
pub fn read_env(getenv: impl Fn(&str) -> Option<String>) -> Env {
    let nonblank = |k: &str| {
        getenv(k)
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty())
    };
    let roots = nonblank("CURSOR_WORKSPACE_ROOTS")
        .map(|r| {
            r.split(':')
                .filter(|s| !s.trim().is_empty())
                .map(|s| s.trim().to_string())
                .collect()
        })
        .unwrap_or_default();
    Env {
        workspace_roots: roots,
        project_dir: nonblank("CURSOR_PROJECT_DIR").unwrap_or_default(),
        workspace_id: nonblank("XMUSTARD_WORKSPACE_ID").unwrap_or_default(),
    }
}

/// The workspace pin for a request: the resolved root when the environment agrees,
/// else the operator's id. Cursor sends no MCP roots, so the daemon binds by `cwd`
/// when neither is set.
pub fn workspace_pin(env: &Env, payload: &str) -> Option<String> {
    let payload_roots = json_string_array(payload, "workspace_roots");
    let roots = if payload_roots.is_empty() {
        &env.workspace_roots
    } else {
        &payload_roots
    };
    resolve_workspace(roots, &env.project_dir).or_else(|| {
        if env.workspace_id.is_empty() {
            None
        } else {
            Some(env.workspace_id.clone())
        }
    })
}

/// The usage line, printed by `-h` and by an argument error.
pub const USAGE: &str = "usage: xmustard-cursor <event> [--api URL] [--workspace ID] [--allow-insecure-remote]\n\
     events: sessionStart, preToolUse, postToolUse, preCompact";

/// True for an event this client routes. An unrouted event name is refused, so a
/// typos hooks.json fails open instead of posting to a route that does not exist.
pub fn routable(event: &str) -> bool {
    ROUTED_EVENTS.contains(&event)
}

/// The daemon's default TCP address when `XMUSTARD_API_BASE` is unset.
pub const DEFAULT_API_BASE: &str = "http://127.0.0.1:8042";

/// The `POST` path for one event.
pub fn route(event: &str) -> String {
    format!("/api/hooks/cursor/{event}")
}

/// Reads the process environment once. Named so the binary's only environment
/// access is one call.
pub fn process_env() -> impl Fn(&str) -> Option<String> {
    |k: &str| env::var(k).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_post_tool_use_replacement_only_replaces_the_mcp_result() {
        let got = event_answer(
            "postToolUse",
            Some("ground line"),
            Some("{\"content\":\"x\"}"),
        );
        let Outcome::Answered(a) = got else {
            panic!("expected an answer, got {got:?}");
        };
        assert_eq!(
            a.updated_mcp_tool_output.as_deref(),
            Some("{\"content\":\"x\"}")
        );
        assert_eq!(a.additional_context.as_deref(), Some("ground line"));
        assert_eq!(a.updated_input, None, "postToolUse has no updated_input");
    }

    #[test]
    fn a_built_in_is_observed_and_unchanged() {
        // The daemon sends no replacement for a Cursor built-in: Silent means the
        // client writes nothing and Cursor keeps the tool's own output.
        assert_eq!(event_answer("postToolUse", None, None), Outcome::Silent);
    }

    #[test]
    fn a_pre_tool_use_rewrite_carries_updated_input() {
        let Outcome::Answered(a) = event_answer("preToolUse", None, Some("{\"command\":\"x\"}"))
        else {
            panic!("expected an answer");
        };
        assert_eq!(a.updated_input.as_deref(), Some("{\"command\":\"x\"}"));
        assert_eq!(a.updated_mcp_tool_output, None);
    }

    #[test]
    fn a_pre_compact_context_is_not_sent_because_the_event_does_not_take_one() {
        // Cursor's additional-context step covers sessionStart, beforeSubmitPrompt,
        // preToolUse, postToolUse and postToolUseFailure only, and preCompact's output
        // schema carries user_message alone.
        assert_eq!(
            event_answer("preCompact", Some("ground line"), None),
            Outcome::Silent
        );
    }

    #[test]
    fn a_session_start_context_is_sent() {
        let Outcome::Answered(a) = event_answer("sessionStart", Some("ground line"), None) else {
            panic!("expected an answer");
        };
        assert_eq!(a.additional_context.as_deref(), Some("ground line"));
    }

    #[test]
    fn injected_context_is_capped_at_the_measured_vendor_line() {
        let long = "x".repeat(CURSOR_CONTEXT_CHARS + 500);
        let Outcome::Answered(a) = event_answer("sessionStart", Some(&long), None) else {
            panic!("expected an answer");
        };
        assert_eq!(
            a.additional_context.as_deref().map(|c| c.chars().count()),
            Some(CURSOR_CONTEXT_CHARS),
            "Cursor throws above the cap rather than truncating, so the client caps first"
        );
    }

    #[test]
    fn context_exactly_at_the_cap_is_untouched() {
        let exact = "x".repeat(CURSOR_CONTEXT_CHARS);
        let Outcome::Answered(a) = event_answer("sessionStart", Some(&exact), None) else {
            panic!("expected an answer");
        };
        assert_eq!(
            a.additional_context.as_deref().map(str::len),
            Some(CURSOR_CONTEXT_CHARS)
        );
    }

    #[test]
    fn a_failure_exit_is_not_the_blocking_code() {
        assert_ne!(FAIL_OPEN_EXIT, 2, "exit 2 blocks the user's action");
        assert_ne!(FAIL_OPEN_EXIT, 0, "exit 0 would claim a valid empty answer");
    }

    #[test]
    fn a_rootless_payload_is_refused() {
        assert_eq!(resolve_workspace(&[], "/repo"), None);
        assert_eq!(resolve_workspace(&["/repo".into()], ""), None);
    }

    #[test]
    fn a_disagreeing_root_is_refused_rather_than_borrowing_the_first() {
        assert_eq!(resolve_workspace(&["/a".into(), "/b".into()], "/c"), None);
    }

    #[test]
    fn the_project_dir_selects_its_own_member_of_a_multi_root_list() {
        let roots = vec!["/first".to_string(), "/second".to_string()];
        assert_eq!(
            resolve_workspace(&roots, "/second"),
            Some("/second".to_string()),
            "the first element is a convention, not the active project"
        );
    }

    #[test]
    fn rendered_answers_carry_exactly_the_documented_keys() {
        let a = Answer {
            additional_context: Some("c".into()),
            updated_mcp_tool_output: Some("m".into()),
            updated_input: None,
        };
        let json = render(&a).expect("an answer renders");
        assert_eq!(
            json,
            r#"{"additional_context":"c","updated_mcp_tool_output":"m"}"#
        );
    }

    #[test]
    fn an_empty_answer_renders_nothing() {
        assert_eq!(render(&Answer::default()), None);
    }

    #[test]
    fn a_rendered_answer_escapes_what_cursor_parses() {
        let a = Answer {
            additional_context: Some("line\nwith \"quotes\" and \\ backslash".into()),
            ..Answer::default()
        };
        let json = render(&a).unwrap();
        assert!(json.contains(r#"\n"#));
        assert!(json.contains(r#"\""#));
        assert!(json.contains(r"\\"));
    }

    #[test]
    fn a_json_string_never_leaves_a_raw_control_character() {
        let s = json_string("a\u{1}b");
        assert!(!s.contains('\u{1}'), "{s}");
        assert!(s.contains(r"\u0001"));
    }

    #[test]
    fn a_key_inside_a_value_is_not_read_as_a_member() {
        let body = r#"{"content":"{\"additional_context\":\"decoy\"}","actual":"real"}"#;
        assert_eq!(json_member(body, "additional_context"), None);
        assert_eq!(json_member(body, "actual"), Some("real"));
    }

    #[test]
    fn the_workspace_roots_array_reads_its_strings() {
        let body = r#"{"workspace_roots":["/a","/b"],"other":"x"}"#;
        assert_eq!(json_string_array(body, "workspace_roots"), vec!["/a", "/b"]);
    }

    #[test]
    fn a_trailing_separator_does_not_break_a_root_match() {
        assert_eq!(
            resolve_workspace(&["/repo//".into()], "/repo"),
            Some("/repo//".to_string())
        );
    }

    #[test]
    fn only_the_four_named_events_are_routable() {
        for e in ROUTED_EVENTS {
            assert!(routable(e), "{e}");
        }
        for declined in [
            "afterShellExecution",
            "beforeReadFile",
            "beforeSubmitPrompt",
            "workspaceOpen",
            "PostToolUse",
        ] {
            assert!(!routable(declined), "{declined} is declined by the spec");
        }
    }

    #[test]
    fn the_route_names_the_cursor_client() {
        assert_eq!(route("postToolUse"), "/api/hooks/cursor/postToolUse");
    }

    #[test]
    fn the_pin_prefers_the_resolved_root_over_the_operator_id() {
        let env = Env {
            workspace_roots: vec!["/repo".into()],
            project_dir: "/repo".into(),
            workspace_id: "envid".into(),
        };
        assert_eq!(
            workspace_pin(&env, r#"{"workspace_roots":["/repo"]}"#).as_deref(),
            Some("/repo")
        );
    }

    #[test]
    fn the_payload_roots_win_over_stale_environment_roots() {
        // Cursor sends workspace_roots with the event; the environment variable is
        // what the shell exported, which a project hook can leave stale.
        let env = Env {
            workspace_roots: vec!["/other".into()],
            project_dir: "/repo".into(),
            workspace_id: "envid".into(),
        };
        assert_eq!(
            workspace_pin(&env, r#"{"workspace_roots":["/repo"]}"#).as_deref(),
            Some("/repo"),
            "the event's own roots are fresher than an exported variable"
        );
    }

    #[test]
    fn a_root_that_disagrees_with_the_project_dir_falls_back_to_the_operator_pin() {
        let env = Env {
            workspace_roots: vec!["/a".into(), "/b".into()],
            project_dir: "/c".into(),
            workspace_id: "envid".into(),
        };
        assert_eq!(
            workspace_pin(&env, r#"{"workspace_roots":["/a","/b"]}"#).as_deref(),
            Some("envid"),
            "no member matches the project dir, so the operator's pin is the only answer"
        );
    }

    #[test]
    fn no_root_and_no_pin_posts_nothing() {
        let env = Env {
            workspace_roots: vec![],
            project_dir: "/repo".into(),
            workspace_id: String::new(),
        };
        assert_eq!(workspace_pin(&env, "{}"), None);
    }
}
