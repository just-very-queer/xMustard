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

/// The daemon's replacement for this event, read out of the daemon's own answer.
///
/// The daemon answers in Cursor's schema (WS-40c-c), so the replacement a Cursor
/// event takes is the member that event's schema names: `updated_mcp_tool_output`
/// for a postToolUse, `updated_input` for a preToolUse. An event that takes no
/// replacement has none, whatever the reply carries (WS-40c-b r1-6: the client read
/// a reply shape the daemon did not write, and every answer was empty).
pub fn replacement_for(event: &str, reply: &str) -> Option<String> {
    match event {
        "postToolUse" => json_member(reply, "updated_mcp_tool_output"),
        "preToolUse" => json_member(reply, "updated_input"),
        _ => None,
    }
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
/// as paths, so a trailing separator or a repeated separator does not make a match
/// fail.
///
/// A `.` root is the process's own directory and matches **only** a project dir that
/// is `.` too (WS-40c-b r1-7: the old code let `.` match every path, so a `.` root
/// bound whatever project Cursor happened to be in). Cursor sends an absolute
/// `CURSOR_PROJECT_DIR`, so the fail-closed reading costs nothing in practice.
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
    clean(a) == clean(b)
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
            // U+2028 and U+2029 are line terminators in JavaScript, so a reader that
            // evaluates its input would break on them even though JSON allows them
            // raw (WS-40c-b r1-8).
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
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
///
/// **Only a member of the top-level object counts** (WS-40c-b r1-5). The old search
/// found the first occurrence of the key anywhere, and a key inside a nested object
/// or an array shadowed the top-level one: `{"outer":{"context":"nested"},"context":"top"}`
/// read the nested value. The scan below walks the object's own members, so a nested
/// key is text, not a member.
///
/// **The value is decoded, not copied** (WS-40c-b r1-8). A member read raw and written
/// into another JSON string is escaped twice: the reply's `{\"content\":[]}` would
/// reach Cursor as `{\\\"content\\\":[]}`, which is not the text the daemon sent.
pub fn json_member(body: &str, key: &str) -> Option<String> {
    let value = top_level_member(body, key)?;
    let value = value.strip_prefix('"')?;
    let end = json_string_end(value)?;
    json_unescape(&value[..end])
}

/// Decodes a JSON string's escapes into the characters they stand for.
///
/// An escape the reader does not know, or a `\u` escape that is not a character, is
/// absent rather than a guess: the member is not one the client can read.
fn json_unescape(value: &str) -> Option<String> {
    let mut out = String::with_capacity(value.len());
    let mut chars = value.chars();
    while let Some(c) = chars.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match chars.next()? {
            '"' => out.push('"'),
            '\\' => out.push('\\'),
            '/' => out.push('/'),
            'b' => out.push('\u{8}'),
            'f' => out.push('\u{c}'),
            'n' => out.push('\n'),
            'r' => out.push('\r'),
            't' => out.push('\t'),
            'u' => {
                let hex: String = (0..4).filter_map(|_| chars.next()).collect();
                if hex.len() != 4 {
                    return None;
                }
                let n = u32::from_str_radix(&hex, 16).ok()?;
                out.push(char::from_u32(n)?);
            }
            _ => return None,
        }
    }
    Some(out)
}

/// The value text of a top-level member, and the offset just past it.
///
/// The value is returned as it stands in the body: a string with its quotes, an
/// object or an array whole. A member that is not there is `None`.
fn top_level_member<'a>(body: &'a str, key: &str) -> Option<&'a str> {
    let bytes = body.as_bytes();
    let mut i = 0usize;
    // the object's own brace
    while i < bytes.len() && bytes[i] != b'{' {
        i += 1;
    }
    i += 1;
    loop {
        i = skip_space(bytes, i);
        match bytes.get(i) {
            Some(b'"') => {}
            _ => return None, // a close, an end, or a value where a key belongs
        }
        let name_start = i + 1;
        let name_end = name_start + json_string_end(&body[name_start..])?;
        let name = &body[name_start..name_end];
        i = skip_space(bytes, name_end + 1);
        if bytes.get(i) != Some(&b':') {
            return None;
        }
        i = skip_space(bytes, i + 1);
        let value_start = i;
        let value_end = value_end(body, i)?;
        if name == key {
            return Some(&body[value_start..value_end]);
        }
        i = skip_space(bytes, value_end);
        match bytes.get(i) {
            Some(b',') => i += 1,
            _ => return None,
        }
    }
}

/// The offset just past the value that starts at `start`: a string, a nested
/// object or array (scanned whole, strings inside them included), or a bare token
/// up to the next comma.
fn value_end(body: &str, start: usize) -> Option<usize> {
    let bytes = body.as_bytes();
    match bytes.get(start)? {
        b'"' => Some(start + 1 + json_string_end(&body[start + 1..])? + 1),
        b'{' | b'[' => {
            let mut depth = 0usize;
            let mut i = start;
            while i < bytes.len() {
                match bytes[i] {
                    b'"' => {
                        i += 1 + json_string_end(&body[i + 1..])? + 1;
                        continue;
                    }
                    b'{' | b'[' => depth += 1,
                    b'}' | b']' => {
                        depth -= 1;
                        if depth == 0 {
                            return Some(i + 1);
                        }
                    }
                    _ => {}
                }
                i += 1;
            }
            None
        }
        _ => {
            let mut i = start;
            while i < bytes.len() && bytes[i] != b',' && bytes[i] != b'}' {
                i += 1;
            }
            Some(i)
        }
    }
}

/// The offset of the first byte at or after `i` that is not JSON whitespace.
fn skip_space(bytes: &[u8], mut i: usize) -> usize {
    while i < bytes.len() && matches!(bytes[i], b' ' | b'\t' | b'\n' | b'\r') {
        i += 1;
    }
    i
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
///
/// The member is the object's own (WS-40c-b r1-5): a `workspace_roots` key inside a
/// nested object is text, not the member, and must not stand in for it.
pub fn json_string_array(body: &str, key: &str) -> Vec<String> {
    let Some(value) = top_level_member(body, key) else {
        return Vec::new();
    };
    let Some(rest) = value.strip_prefix('[') else {
        return Vec::new();
    };
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
                    // decoded, like every other member: a path read raw would carry
                    // its escapes into the comparison
                    match json_unescape(raw) {
                        Some(v) => out.push(v),
                        None => return out,
                    }
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

/// The workspace pin for a request: the configured workspace id, when the roots
/// agree with the project dir.
///
/// The pin is an **id**, never a path (WS-40c-b r1-2). The daemon's `X-Xmustard-Workspace`
/// header is a workspace-id channel: it validates the id, looks the workspace up, and
/// refuses one that is not in the caller's token scope. A path in that header is
/// either refused as an unsafe id or names nothing, so the client sends an id.
///
/// The roots are the client's own check that the configured workspace is the one
/// Cursor is running in (WS-40c-a): `CURSOR_PROJECT_DIR` must match a member of
/// `workspace_roots`, and a disagreement sends nothing rather than binding the wrong
/// repository (WS-40c-b r1-3: the old code fell back to the id here, which posted
/// the event to a workspace the project dir disagreed with).
pub fn workspace_pin(env: &Env, payload: &str, configured: Option<&str>) -> Option<String> {
    let id = configured
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| env.workspace_id.trim());
    if id.is_empty() || !valid_workspace_id(id) {
        return None;
    }
    let payload_roots = json_string_array(payload, "workspace_roots");
    let roots = if payload_roots.is_empty() {
        &env.workspace_roots
    } else {
        &payload_roots
    };
    if !roots_agree(roots, &env.project_dir) {
        return None;
    }
    Some(id.to_string())
}

/// Whether a workspace id is one the daemon accepts: the same rule the daemon's
/// transport applies (safe characters, at most 128 of them, and no `..`).
pub fn valid_workspace_id(id: &str) -> bool {
    if id.is_empty() || id.len() > 128 || id.contains("..") {
        return false;
    }
    id.chars()
        .all(|c| c.is_ascii_alphanumeric() || matches!(c, '_' | '.' | '-'))
}

/// Whether the roots agree with the project dir: the project dir names one of them.
///
/// An empty list agrees with nothing (WS-40c-a refuses an empty list), and a project
/// dir that names no root is a disagreement. Both send nothing.
fn roots_agree(roots: &[String], project_dir: &str) -> bool {
    resolve_workspace(roots, project_dir).is_some()
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

    /// The measured vendor line, written as a literal on purpose.
    ///
    /// Every expected value here is the number from Cursor's own bundle
    /// (`limits.ts`, `Cbt=1e4`), not `CURSOR_CONTEXT_CHARS`: a property asserted
    /// against the constant it is testing moves with it, so mutation M2 (raising the
    /// cap to 12,000) passed every assertion before this was corrected.
    const MEASURED_CAP: usize = 10_000;

    #[test]
    fn the_cap_is_the_measured_vendor_line() {
        assert_eq!(
            CURSOR_CONTEXT_CHARS, MEASURED_CAP,
            "Cursor's additional_context cap is 10,000 characters (limits.ts, Cbt=1e4); \
             a different number here is a claim with no measurement behind it"
        );
    }

    #[test]
    fn injected_context_is_capped_at_the_measured_vendor_line() {
        let long = "x".repeat(MEASURED_CAP + 500);
        let Outcome::Answered(a) = event_answer("sessionStart", Some(&long), None) else {
            panic!("expected an answer");
        };
        assert_eq!(
            a.additional_context.as_deref().map(|c| c.chars().count()),
            Some(MEASURED_CAP),
            "Cursor throws above the cap rather than truncating, so the client caps first"
        );
    }

    #[test]
    fn context_exactly_at_the_cap_is_untouched() {
        let exact = "x".repeat(MEASURED_CAP);
        let Outcome::Answered(a) = event_answer("sessionStart", Some(&exact), None) else {
            panic!("expected an answer");
        };
        assert_eq!(
            a.additional_context.as_deref().map(str::len),
            Some(MEASURED_CAP)
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
        assert_eq!(json_member(body, "actual").as_deref(), Some("real"));
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
    fn the_pin_is_the_workspace_id_when_the_roots_agree() {
        let env = Env {
            workspace_roots: vec!["/repo".into()],
            project_dir: "/repo".into(),
            workspace_id: "envid".into(),
        };
        assert_eq!(
            workspace_pin(&env, r#"{"workspace_roots":["/repo"]}"#, None).as_deref(),
            Some("envid"),
            "the pin is an id, and the roots agree with the project dir"
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
            workspace_pin(&env, r#"{"workspace_roots":["/repo"]}"#, None).as_deref(),
            Some("envid"),
            "the event's own roots are fresher than an exported variable"
        );
    }

    #[test]
    fn a_root_that_disagrees_with_the_project_dir_sends_nothing() {
        let env = Env {
            workspace_roots: vec!["/a".into(), "/b".into()],
            project_dir: "/c".into(),
            workspace_id: "envid".into(),
        };
        assert_eq!(
            workspace_pin(&env, r#"{"workspace_roots":["/a","/b"]}"#, None),
            None,
            "no member matches the project dir, so nothing is sent (WS-40c-b r1-3): \
             the operator's id would have bound the wrong repository"
        );
    }

    #[test]
    fn a_configured_id_wins_over_the_environment_one() {
        let env = Env {
            workspace_roots: vec!["/repo".into()],
            project_dir: "/repo".into(),
            workspace_id: "envid".into(),
        };
        assert_eq!(
            workspace_pin(&env, r#"{"workspace_roots":["/repo"]}"#, Some("flagid")).as_deref(),
            Some("flagid")
        );
    }

    #[test]
    fn no_root_and_no_pin_posts_nothing() {
        let env = Env {
            workspace_roots: vec![],
            project_dir: "/repo".into(),
            workspace_id: String::new(),
        };
        assert_eq!(workspace_pin(&env, "{}", None), None);
    }
}
