//! The Cursor adapter's fixtures against the real client (WS-40c-b; PAR-ADP-10).
//!
//! Every answer below comes from `cursorhook`, the same code the binary runs, so a
//! change to the answer contract fails here rather than in a Cursor session nobody can
//! script. The inputs are source-derived (see `integrations/cursor/testdata/manifest.json`
//! for the bundle and version they came from and why they are not recorded).

use std::path::{Path, PathBuf};

use xmustard_core::cursorhook::{self, Answer, Outcome};

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
/// The binary maps the daemon's shared hook result (`context`, `updated`) through
/// `event_answer`, so the fixture asserts that mapping rather than a hand-written
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
        cursorhook::json_member(&body, "cursor_version"),
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
    assert_eq!(cursorhook::json_member(&body, "tool_name"), Some("Shell"));
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
        Some("auto"),
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
            workspace_id: String::new(),
        };
        assert_eq!(
            cursorhook::workspace_pin(&env, &body).as_deref(),
            Some("/repo"),
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
        workspace_id: String::new(),
    };
    assert_eq!(
        cursorhook::workspace_pin(&env, &body),
        None,
        "the fixture's root is /repo, so a project dir of /elsewhere binds nothing"
    );
}

#[test]
fn the_manifest_names_the_version_the_fixtures_came_from() {
    let manifest = read("manifest.json");
    assert_eq!(
        cursorhook::json_member(&manifest, "cursor_version"),
        Some("3.22.7")
    );
    assert!(
        manifest.contains("\"additional_context_cap_chars\": 10000"),
        "the cap the client enforces is recorded where a later session can find it"
    );
    assert_eq!(
        cursorhook::json_member(&manifest, "derivation"),
        Some("source-derived")
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
