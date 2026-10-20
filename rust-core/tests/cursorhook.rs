//! The Cursor adapter's fixtures against the real client (WS-40c-b; PAR-ADP-10).
//!
//! Every answer below comes from `cursorhook`, the same code the binary runs, so a
//! change to the answer contract fails here rather than in a Cursor session nobody can
//! script. The inputs are source-derived (see `integrations/cursor/testdata/manifest.json`
//! for the bundle and version they came from and why they are not recorded).

use std::path::{Path, PathBuf};

use xmustard_core::cursorhook;

fn testdata() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("rust-core has a parent")
        .join("integrations/cursor/testdata")
}

fn read(name: &str) -> String {
    let path = testdata().join(name);
    std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
}

/// The answer `xmustard-cursor` prints for one fixture, given the daemon's reply.
///
/// The daemon answers in Cursor's schema (WS-40c-c): the context is
/// `additional_context` and the replacement is the member the event's own schema
/// names. The fixture asserts the mapping the binary runs rather than a hand-written
/// expectation: a golden that disagreed with the client would only be testing the
/// fixture.
fn answer_for(event: &str, context: Option<&str>, updated: Option<&str>) -> Option<String> {
    let cursorhook::Outcome::Answered(a) = cursorhook::event_answer(event, context, updated) else {
        return None;
    };
    cursorhook::render(&a)
}

#[test]
fn a_session_start_answer_carries_only_the_context() {
    let body = read("01-sessionStart.in.json");
    assert_eq!(
        answer_for("sessionStart", Some("2 failed runs"), None).as_deref(),
        Some(r#"{"additional_context":"2 failed runs"}"#)
    );
    assert_eq!(
        cursorhook::json_member(&body, "cursor_version").as_deref(),
        Some("3.22.7")
    );
}

#[test]
fn a_shell_pre_tool_use_rewrite_carries_updated_input() {
    let body = read("02-preToolUse-shell.in.json");
    let got = answer_for(
        "preToolUse",
        Some("3 memories"),
        Some(r#"{"command":"rg -n TODO api-go --stats"}"#),
    );
    assert_eq!(
        got.as_deref(),
        Some(
            r#"{"additional_context":"3 memories","updated_input":"{\"command\":\"rg -n TODO api-go --stats\"}"}"#
        )
    );
    // The fixture is a Shell call, which is the only tool the matcher names.
    assert_eq!(
        cursorhook::json_member(&body, "tool_name").as_deref(),
        Some("Shell")
    );
}

#[test]
fn an_mcp_post_tool_use_replaces_the_result() {
    let body = read("03-postToolUse-mcp.in.json");
    let got = answer_for(
        "postToolUse",
        None,
        Some(r#"{"content":[{"type":"text","text":"2 lines"}]}"#),
    );
    assert_eq!(
        got.as_deref(),
        Some(
            r#"{"updated_mcp_tool_output":"{\"content\":[{\"type\":\"text\",\"text\":\"2 lines\"}]}"}"#
        )
    );
    // tool_output is a JSON string, not raw terminal text: the shaper decides, the
    // client only relays, and this fixture is the shape the shaper must decode.
    assert!(
        cursorhook::json_member(&body, "tool_output")
            .is_some_and(|o| o.starts_with('{') || o.starts_with("{\\\""))
    );
}

#[test]
fn a_pre_compact_sends_nothing() {
    let body = read("04-preCompact.in.json");
    assert_eq!(answer_for("preCompact", Some("2 failed runs"), None), None);
    assert_eq!(
        cursorhook::json_member(&body, "trigger"),
        Some("auto".to_string()),
        "the fixture keeps the observational fields Cursor sends"
    );
}

#[test]
fn every_fixture_binds_to_its_own_workspace() {
    for name in [
        "01-sessionStart.in.json",
        "02-preToolUse-shell.in.json",
        "03-postToolUse-mcp.in.json",
        "04-preCompact.in.json",
    ] {
        let body = read(name);
        let env = cursorhook::Env {
            workspace_roots: Vec::new(),
            project_dir: "/repo".into(),
            workspace_id: "ws-cursor".into(),
        };
        // The pin is a workspace id, never a path (WS-40c-b r1-2): the daemon's
        // header is a workspace-id channel, and it validates what it carries.
        assert_eq!(
            cursorhook::workspace_pin(&env, &body, None).as_deref(),
            Some("ws-cursor"),
            "{name}"
        );
    }
}

#[test]
fn a_root_the_project_dir_disagrees_with_binds_to_nothing() {
    let body = read("01-sessionStart.in.json");
    let env = cursorhook::Env {
        workspace_roots: Vec::new(),
        project_dir: "/elsewhere".into(),
        workspace_id: "ws-cursor".into(),
    };
    assert_eq!(
        cursorhook::workspace_pin(&env, &body, None),
        None,
        "the fixture's root is /repo, so a project dir of /elsewhere binds nothing, \
         and a configured id does not make the disagreement sendable (WS-40c-b r1-3)"
    );
}

/// A pin the daemon would refuse is not sent: the id is the same rule the daemon's
/// transport applies, and a path is not an id (WS-40c-b r1-2).
#[test]
fn a_pin_the_daemon_would_refuse_is_not_sent() {
    let body = read("01-sessionStart.in.json");
    let env = cursorhook::Env {
        workspace_roots: Vec::new(),
        project_dir: "/repo".into(),
        workspace_id: String::new(),
    };
    for bad in ["/repo", "..", "a b", "ws\r\nX: 1", ""] {
        assert!(
            !cursorhook::valid_workspace_id(bad),
            "{bad:?} is not a workspace id"
        );
        assert_eq!(
            cursorhook::workspace_pin(&env, &body, Some(bad)),
            None,
            "{bad:?} must not be sent as a pin"
        );
    }
    assert!(cursorhook::valid_workspace_id("ws-cursor"));
}

#[test]
fn the_manifest_names_the_version_the_fixtures_came_from() {
    let manifest = read("manifest.json");
    assert_eq!(
        cursorhook::json_member(&manifest, "cursor_version").as_deref(),
        Some("3.22.7")
    );
    assert!(
        manifest.contains("\"additional_context_cap_chars\": 10000"),
        "the cap the client enforces is recorded where a later session can find it"
    );
    assert_eq!(
        cursorhook::json_member(&manifest, "derivation"),
        Some("source-derived".to_string())
    );
}

#[test]
fn the_hooks_json_fails_open_and_names_the_four_routed_events() {
    let hooks = std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .parent()
            .expect("rust-core has a parent")
            .join("integrations/cursor/hooks.json"),
    )
    .expect("integrations/cursor/hooks.json is present");
    // Cursor's own defaults are loop_limit 5 and failClosed false; the file states them
    // so the policy it was written against is visible in it.
    assert_eq!(hooks.matches("\"failClosed\": false").count(), 4);
    for event in cursorhook::ROUTED_EVENTS {
        assert!(
            hooks.contains(&format!("xmustard-cursor {event}")),
            "{event}"
        );
    }
    assert!(
        !hooks.contains("\"failClosed\": true"),
        "the owner's answer is fail open"
    );
    // preToolUse is registered for Shell alone: updated_input outside Write/Task is a
    // reported Cursor bug, and a bug report is not a source.
    assert!(hooks.contains("\"matcher\": \"Shell\""));
}

/// A key inside a nested object is text, not a member: it must not shadow the
/// top-level member of the same name (WS-40c-b r1-5).
#[test]
fn a_nested_member_does_not_shadow_the_top_level_one() {
    let body = r#"{"outer":{"additional_context":"nested"},"additional_context":"top"}"#;
    assert_eq!(
        cursorhook::json_member(body, "additional_context").as_deref(),
        Some("top")
    );
    let array = r#"{"items":[{"additional_context":"nested"}],"additional_context":"top"}"#;
    assert_eq!(
        cursorhook::json_member(array, "additional_context").as_deref(),
        Some("top")
    );
    // A member that is not there is absent, never a guess.
    assert_eq!(
        cursorhook::json_member(r#"{"a":"b"}"#, "additional_context"),
        None
    );
}

/// The daemon answers in Cursor's schema, and the client reads the member the
/// event's own schema names (WS-40c-b r1-6, WS-40c-c).
#[test]
fn the_reply_is_read_in_cursors_schema() {
    let reply = r#"{"additional_context":"ctx","updated_mcp_tool_output":"{\"content\":[]}"}"#;
    assert_eq!(
        cursorhook::json_member(reply, "additional_context").as_deref(),
        Some("ctx")
    );
    assert_eq!(
        cursorhook::replacement_for("postToolUse", reply).as_deref(),
        Some("{\"content\":[]}")
    );
    // An event that takes no replacement has none, whatever the reply carries.
    assert_eq!(cursorhook::replacement_for("sessionStart", reply), None);
    assert_eq!(cursorhook::replacement_for("preCompact", reply), None);
}

/// A `.` root is the process's own directory and matches only a `.` project dir,
/// not every path (WS-40c-b r1-7).
#[test]
fn a_dot_root_matches_only_a_dot_project_dir() {
    let env = cursorhook::Env {
        workspace_roots: Vec::new(),
        project_dir: "/somewhere/else".into(),
        workspace_id: "ws-cursor".into(),
    };
    let body = r#"{"workspace_roots":["."],"hook_event_name":"sessionStart"}"#;
    assert_eq!(
        cursorhook::workspace_pin(&env, body, None),
        None,
        "a . root must not bind whatever project Cursor is in"
    );
}

/// The shipped matchers and the fixtures' own tools must agree: a fixture whose tool
/// name the matcher does not match would never fire the hook, and the fixture would
/// be testing a path Cursor never takes (WS-40c-b r1-10).
#[test]
fn the_shipped_matchers_agree_with_the_fixtures() {
    let hooks = std::fs::read_to_string(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .parent()
            .expect("rust-core has a parent")
            .join("integrations/cursor/hooks.json"),
    )
    .expect("integrations/cursor/hooks.json is present");
    for (fixture, matcher) in [
        ("02-preToolUse-shell.in.json", "Shell"),
        ("03-postToolUse-mcp.in.json", "MCP:.+"),
    ] {
        let tool = cursorhook::json_member(&read(fixture), "tool_name")
            .unwrap_or_else(|| panic!("{fixture} names no tool"));
        assert!(
            hooks.contains(&format!("\"matcher\": \"{matcher}\"")),
            "{fixture}: the shipped hooks.json has no {matcher} matcher"
        );
        // A matcher is a pattern; the tool name must satisfy the part of it that is
        // literal, so a fixture is never a tool the hook would not see.
        let literal = matcher.split('.').next().unwrap_or(matcher);
        let literal = literal.strip_suffix('+').unwrap_or(literal);
        assert!(
            tool.starts_with(literal),
            "{fixture}: the tool {tool} does not match {matcher}"
        );
    }
}
