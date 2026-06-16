//! Swarm scaffold: multiple role-tagged worker lanes over one goal, with a
//! controller gate. This is the faithful, non-magical `/swarm` described in
//! `docs/GOAL_RUNTIME.md`:
//!
//! ```text
//! goal
//!   reader   lane: read-only summary and candidate files
//!   builder  lane: patch draft inside allowed surface
//!   critic   lane: diff review and risk notes
//!   verifier lane: deterministic command evidence
//!   controller gate: accept, block, narrow, or complete
//! ```
//!
//! A swarm is *not* a new store and *not* automation. It is a typed view over a
//! goal's iterations (each tagged with a role) plus a deterministic controller
//! decision. The controller — xMustard — never auto-accepts worker output: the
//! gate only reaches `complete` when a verifier lane carries verification
//! evidence and a critic lane has reviewed.

use crate::goalruntime::{
    self, GoalError, GoalEvidence, GoalIterationAppendRequest, GoalIterationRecord, GoalRecord,
    Result,
};
use serde::Serialize;
use std::path::Path;

/// The worker roles a swarm coordinates. `Controller` is reserved for xMustard's
/// own gate decisions and is not a delegated worker lane.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum SwarmRole {
    Reader,
    Builder,
    Critic,
    Verifier,
    Controller,
}

impl SwarmRole {
    pub fn as_str(self) -> &'static str {
        match self {
            SwarmRole::Reader => "reader",
            SwarmRole::Builder => "builder",
            SwarmRole::Critic => "critic",
            SwarmRole::Verifier => "verifier",
            SwarmRole::Controller => "controller",
        }
    }

    pub fn parse(value: &str) -> Result<Self> {
        match value.trim().to_ascii_lowercase().as_str() {
            "reader" => Ok(SwarmRole::Reader),
            "builder" => Ok(SwarmRole::Builder),
            "critic" => Ok(SwarmRole::Critic),
            "verifier" => Ok(SwarmRole::Verifier),
            "controller" => Ok(SwarmRole::Controller),
            other => Err(GoalError::Validation(format!("invalid swarm role {other:?}"))),
        }
    }

    /// The delegated worker lanes, in execution order.
    pub fn worker_lanes() -> [SwarmRole; 4] {
        [
            SwarmRole::Reader,
            SwarmRole::Builder,
            SwarmRole::Critic,
            SwarmRole::Verifier,
        ]
    }
}

/// The controller's four possible decisions over the current lane evidence.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum SwarmDecision {
    /// Progress is real but the goal is not provably done; keep going.
    Accept,
    /// A lane reported a failure; stop and resolve it before more work.
    Block,
    /// A builder lane edited outside the allowed surface; narrow the scope.
    Narrow,
    /// Verifier evidence and critic review are both present; safe to complete.
    Complete,
}

impl SwarmDecision {
    pub fn as_str(self) -> &'static str {
        match self {
            SwarmDecision::Accept => "accept",
            SwarmDecision::Block => "block",
            SwarmDecision::Narrow => "narrow",
            SwarmDecision::Complete => "complete",
        }
    }
}

/// Per-role lane rollup.
#[derive(Debug, Clone, Serialize)]
pub struct SwarmLane {
    pub role: SwarmRole,
    pub iterations: usize,
    pub has_evidence: bool,
    pub has_verification: bool,
    pub last_outcome: String,
}

/// The controller gate result over all lanes.
#[derive(Debug, Clone, Serialize)]
pub struct SwarmGate {
    pub goal_id: String,
    pub decision: SwarmDecision,
    pub ready_to_complete: bool,
    pub reasons: Vec<String>,
    pub lanes: Vec<SwarmLane>,
    /// Files a builder lane touched that fall outside the goal's allowed surface.
    pub surface_violations: Vec<String>,
}

const FAILURE_OUTCOMES: &[&str] = &["fail", "failed", "error", "blocked", "regression"];

fn outcome_is_failure(outcome: &str) -> bool {
    let lower = outcome.trim().to_ascii_lowercase();
    FAILURE_OUTCOMES.iter().any(|f| lower == *f)
}

/// Append a role-tagged iteration to a goal. The role is validated against the
/// closed [`SwarmRole`] set, then the normal goal-runtime anti-slop guardrails
/// apply (empty/refusal summaries are refused).
pub fn record_lane(
    data_dir: &Path,
    workspace_id: &str,
    goal_id: &str,
    role: SwarmRole,
    mut request: GoalIterationAppendRequest,
) -> Result<(GoalIterationRecord, goalruntime::slop::SlopReport)> {
    // Force the role tag so a worker cannot mislabel its lane.
    request.role = role.as_str().to_string();
    goalruntime::append_iteration(data_dir, workspace_id, goal_id, &request)
}

fn lane_for(role: SwarmRole, iterations: &[GoalIterationRecord]) -> SwarmLane {
    let mine: Vec<&GoalIterationRecord> = iterations
        .iter()
        .filter(|it| it.role.trim().eq_ignore_ascii_case(role.as_str()))
        .collect();
    let has_verification = mine
        .iter()
        .any(|it| it.evidence.iter().any(GoalEvidence::is_verification));
    let has_evidence = mine.iter().any(|it| !it.evidence.is_empty());
    let last_outcome = mine
        .last()
        .map(|it| it.outcome.clone())
        .unwrap_or_default();
    SwarmLane {
        role,
        iterations: mine.len(),
        has_evidence,
        has_verification,
        last_outcome,
    }
}

/// Compute the controller gate for a goal's current swarm state. Pure decision
/// over the durable iterations — no side effects.
pub fn gate(data_dir: &Path, workspace_id: &str, goal_id: &str) -> Result<SwarmGate> {
    let goal = goalruntime::get_goal(data_dir, workspace_id, goal_id)?;
    let iterations = goalruntime::list_iterations(data_dir, workspace_id, goal_id)?;
    Ok(gate_over(&goal, &iterations))
}

/// Gate logic isolated from IO so it is trivially testable.
pub fn gate_over(goal: &GoalRecord, iterations: &[GoalIterationRecord]) -> SwarmGate {
    let lanes: Vec<SwarmLane> = SwarmRole::worker_lanes()
        .iter()
        .map(|role| lane_for(*role, iterations))
        .collect();

    // Surface violations: builder lanes touching files outside allowed_surface.
    let mut surface_violations = Vec::new();
    if !goal.allowed_surface.is_empty() {
        for it in iterations
            .iter()
            .filter(|it| it.role.eq_ignore_ascii_case("builder"))
        {
            for touched in &it.files_touched {
                let allowed = goal.allowed_surface.iter().any(|surface| {
                    touched == surface
                        || touched.starts_with(&format!("{}/", surface.trim_end_matches('/')))
                });
                if !allowed && !surface_violations.contains(touched) {
                    surface_violations.push(touched.clone());
                }
            }
        }
    }

    let has_block = iterations.iter().any(|it| outcome_is_failure(&it.outcome));
    let verifier_ok = lanes
        .iter()
        .find(|l| l.role == SwarmRole::Verifier)
        .map(|l| l.has_verification)
        .unwrap_or(false);
    let critic_done = lanes
        .iter()
        .find(|l| l.role == SwarmRole::Critic)
        .map(|l| l.iterations > 0)
        .unwrap_or(false);

    let mut reasons = Vec::new();
    let decision = if has_block {
        for it in iterations.iter().filter(|it| outcome_is_failure(&it.outcome)) {
            reasons.push(format!(
                "{} lane reported '{}': {}",
                if it.role.is_empty() { "worker" } else { &it.role },
                it.outcome,
                it.summary
            ));
        }
        SwarmDecision::Block
    } else if !surface_violations.is_empty() {
        reasons.push(format!(
            "builder edited outside allowed surface: {}",
            surface_violations.join(", ")
        ));
        SwarmDecision::Narrow
    } else if verifier_ok && critic_done {
        reasons.push("verifier lane has verification evidence".to_string());
        reasons.push("critic lane has reviewed".to_string());
        SwarmDecision::Complete
    } else {
        if !verifier_ok {
            reasons.push("waiting on verifier lane for verification evidence".to_string());
        }
        if !critic_done {
            reasons.push("waiting on critic lane for a review pass".to_string());
        }
        if reasons.is_empty() {
            reasons.push("progress recorded; goal not yet provably complete".to_string());
        }
        SwarmDecision::Accept
    };

    SwarmGate {
        goal_id: goal.goal_id.clone(),
        decision,
        ready_to_complete: decision == SwarmDecision::Complete,
        reasons,
        lanes,
        surface_violations,
    }
}

/// A swarm plan: the lanes in order plus their current fill state and the gate.
#[derive(Debug, Clone, Serialize)]
pub struct SwarmPlan {
    pub goal_id: String,
    pub title: String,
    pub status: String,
    pub lanes: Vec<SwarmLane>,
    pub gate: SwarmGate,
}

/// Build the swarm plan/status view for a goal.
pub fn plan(data_dir: &Path, workspace_id: &str, goal_id: &str) -> Result<SwarmPlan> {
    let goal = goalruntime::get_goal(data_dir, workspace_id, goal_id)?;
    let iterations = goalruntime::list_iterations(data_dir, workspace_id, goal_id)?;
    let gate = gate_over(&goal, &iterations);
    Ok(SwarmPlan {
        goal_id: goal.goal_id.clone(),
        title: goal.title.clone(),
        status: goal.status.as_str().to_string(),
        lanes: gate.lanes.clone(),
        gate,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::goalruntime::{GoalCreateRequest, GoalStatus};
    use tempfile::TempDir;

    fn seed_goal(dir: &Path) -> String {
        let req = GoalCreateRequest {
            title: "Swarm demo".to_string(),
            objective: "exercise the swarm controller gate end to end".to_string(),
            allowed_surface: vec!["rust-core/src".to_string()],
            ..Default::default()
        };
        goalruntime::create_goal(dir, "ws", &req).unwrap().0.goal_id
    }

    // `_role` documents the lane at the call site; `record_lane` sets the
    // authoritative role tag, so the request itself does not carry it.
    fn iter(_role: SwarmRole, summary: &str) -> GoalIterationAppendRequest {
        GoalIterationAppendRequest {
            summary: summary.to_string(),
            ..Default::default()
        }
    }

    #[test]
    fn role_round_trips() {
        for role in SwarmRole::worker_lanes() {
            assert_eq!(SwarmRole::parse(role.as_str()).unwrap(), role);
        }
        assert!(SwarmRole::parse("bogus").is_err());
    }

    #[test]
    fn fresh_goal_gate_accepts_and_waits() {
        let dir = TempDir::new().unwrap();
        let id = seed_goal(dir.path());
        let g = gate(dir.path(), "ws", &id).unwrap();
        assert_eq!(g.decision, SwarmDecision::Accept);
        assert!(!g.ready_to_complete);
        assert!(g.reasons.iter().any(|r| r.contains("verifier")));
        assert!(g.reasons.iter().any(|r| r.contains("critic")));
    }

    #[test]
    fn complete_requires_verifier_evidence_and_critic() {
        let dir = TempDir::new().unwrap();
        let id = seed_goal(dir.path());
        record_lane(dir.path(), "ws", &id, SwarmRole::Reader, iter(SwarmRole::Reader, "Mapped the candidate files")).unwrap();
        record_lane(dir.path(), "ws", &id, SwarmRole::Builder, iter(SwarmRole::Builder, "Drafted the patch in scope")).unwrap();
        // Only critic so far -> still Accept (no verifier evidence).
        record_lane(dir.path(), "ws", &id, SwarmRole::Critic, iter(SwarmRole::Critic, "Reviewed the diff, low risk")).unwrap();
        assert_eq!(gate(dir.path(), "ws", &id).unwrap().decision, SwarmDecision::Accept);
        // Verifier with real evidence -> Complete.
        let mut v = iter(SwarmRole::Verifier, "Ran the suite, all green");
        v.evidence = vec![GoalEvidence {
            kind: "test".to_string(),
            command: "cargo test".to_string(),
            outcome: "pass".to_string(),
            ..Default::default()
        }];
        record_lane(dir.path(), "ws", &id, SwarmRole::Verifier, v).unwrap();
        let g = gate(dir.path(), "ws", &id).unwrap();
        assert_eq!(g.decision, SwarmDecision::Complete);
        assert!(g.ready_to_complete);
    }

    #[test]
    fn failure_outcome_blocks() {
        let dir = TempDir::new().unwrap();
        let id = seed_goal(dir.path());
        let mut b = iter(SwarmRole::Builder, "Attempted the patch but the build broke");
        b.outcome = "failed".to_string();
        record_lane(dir.path(), "ws", &id, SwarmRole::Builder, b).unwrap();
        let g = gate(dir.path(), "ws", &id).unwrap();
        assert_eq!(g.decision, SwarmDecision::Block);
        assert!(g.reasons.iter().any(|r| r.contains("failed")));
    }

    #[test]
    fn out_of_surface_builder_narrows() {
        let dir = TempDir::new().unwrap();
        let id = seed_goal(dir.path());
        let mut b = iter(SwarmRole::Builder, "Edited a file outside the allowed surface");
        b.files_touched = vec!["frontend/src/App.tsx".to_string(), "rust-core/src/swarm.rs".to_string()];
        record_lane(dir.path(), "ws", &id, SwarmRole::Builder, b).unwrap();
        let g = gate(dir.path(), "ws", &id).unwrap();
        assert_eq!(g.decision, SwarmDecision::Narrow);
        assert_eq!(g.surface_violations, vec!["frontend/src/App.tsx".to_string()]);
    }

    #[test]
    fn record_lane_forces_role_and_keeps_antislop() {
        let dir = TempDir::new().unwrap();
        let id = seed_goal(dir.path());
        // Anti-slop still applies through the swarm path.
        let bad = iter(SwarmRole::Reader, "As an AI, I cannot");
        assert!(matches!(
            record_lane(dir.path(), "ws", &id, SwarmRole::Reader, bad),
            Err(GoalError::SlopBlocked(_))
        ));
        // A real one is tagged with the lane role regardless of request.role.
        let mut good = iter(SwarmRole::Builder, "Implemented the gate logic");
        good.role = "lying-role".to_string();
        let (rec, report) = record_lane(dir.path(), "ws", &id, SwarmRole::Builder, good).unwrap();
        assert_eq!(rec.role, "builder");
        assert!(report.ok);
    }

    #[test]
    fn plan_reports_all_four_lanes() {
        let dir = TempDir::new().unwrap();
        let id = seed_goal(dir.path());
        let plan = plan(dir.path(), "ws", &id).unwrap();
        assert_eq!(plan.lanes.len(), 4);
        assert_eq!(plan.status, GoalStatus::Draft.as_str());
    }

    fn make_goal(allowed_surface: &[&str]) -> GoalRecord {
        GoalRecord {
            goal_id: "g".to_string(),
            workspace_id: String::new(),
            title: "test".to_string(),
            objective: "test".to_string(),
            status: GoalStatus::Draft,
            acceptance_criteria: vec![],
            current_tranche: String::new(),
            allowed_surface: allowed_surface.iter().map(|s| s.to_string()).collect(),
            verification_commands: vec![],
            verification_profile_ids: vec![],
            runtime_preference: String::new(),
            preferred_model: String::new(),
            resumption_notes: String::new(),
            evidence: vec![],
            created_at: String::new(),
            updated_at: String::new(),
            completed_at: None,
        }
    }

    #[test]
    fn swarm_gate_edge_cases() {
        // Case 1: Verifier with note evidence (outcome "success", no command/path/url) -> Accept.
        // The evidence lacks a real anchor so is_verification returns false, and gate_over
        // does not reach Complete even though a critic lane has reviewed.
        {
            let goal = make_goal(&["rust-core/src"]);
            let critic = GoalIterationRecord {
                role: "critic".to_string(),
                summary: "Reviewed the diff, looks good".to_string(),
                ..Default::default()
            };
            let verifier = GoalIterationRecord {
                role: "verifier".to_string(),
                summary: "Ran verification pass".to_string(),
                evidence: vec![GoalEvidence {
                    kind: String::new(),
                    outcome: "success".to_string(),
                    ..Default::default()
                }],
                ..Default::default()
            };
            let gate = gate_over(&goal, &[critic, verifier]);
            assert_eq!(
                gate.decision,
                SwarmDecision::Accept,
                "verifier with unverifiable note evidence must NOT reach Complete"
            );
            assert!(!gate.ready_to_complete);
        }

        // Case 2: Builder touching a file exactly matching the allowed_surface prefix.
        // allowed_surface = ["rust-core/src"], touched = "rust-core/src/swarm.rs".
        // Since "rust-core/src/swarm.rs".starts_with("rust-core/src/") is true,
        // this is NOT a surface violation and the decision is NOT Narrow.
        {
            let goal = make_goal(&["rust-core/src"]);
            let builder = GoalIterationRecord {
                role: "builder".to_string(),
                summary: "Edited swarm.rs inside allowed surface".to_string(),
                files_touched: vec!["rust-core/src/swarm.rs".to_string()],
                ..Default::default()
            };
            let gate = gate_over(&goal, &[builder]);
            assert_ne!(
                gate.decision,
                SwarmDecision::Narrow,
                "builder touching file that matches allowed_surface prefix must NOT trigger Narrow"
            );
            assert!(
                gate.surface_violations.is_empty(),
                "expected no surface violations for file matching allowed_surface prefix"
            );
        }

        // Case 3: Both a failure outcome AND an out-of-surface builder edit.
        // has_block (failure) is checked before surface_violations, so the decision is Block.
        {
            let goal = make_goal(&["rust-core/src"]);
            let builder = GoalIterationRecord {
                role: "builder".to_string(),
                summary: "Tried the patch but got a regression".to_string(),
                outcome: "failed".to_string(),
                files_touched: vec!["frontend/src/main.ts".to_string()],
                ..Default::default()
            };
            let gate = gate_over(&goal, &[builder]);
            assert_eq!(
                gate.decision,
                SwarmDecision::Block,
                "failure outcome must take priority over surface violation -> Block, not Narrow"
            );
        }
    }
}
