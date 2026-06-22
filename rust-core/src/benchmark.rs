//! Deterministic micro-benchmarks for the goal/swarm runtime. These measure the
//! durable-store hot paths (create, iterate, list, ledger render, gate decision)
//! so regressions in IO or serialization are visible as numbers, not vibes.
//!
//! The harness is dependency-free (uses `std::time::Instant`) and self-cleaning:
//! it runs against a scratch directory the caller owns.

use crate::goalruntime::{
    self, GoalCreateRequest, GoalEvidence, GoalIterationAppendRequest, Result,
};
use crate::swarm;
use serde::Serialize;
use std::path::Path;
use std::time::Instant;

/// Timing rollup for one benchmarked operation.
#[derive(Debug, Clone, Serialize)]
pub struct BenchOp {
    pub op: String,
    pub iterations: usize,
    pub total_ms: f64,
    pub ops_per_sec: f64,
    pub mean_us: f64,
    pub p50_us: f64,
    pub p99_us: f64,
}

/// Full benchmark report.
#[derive(Debug, Clone, Serialize)]
pub struct BenchReport {
    pub iterations: usize,
    pub ops: Vec<BenchOp>,
}

fn summarize(op: &str, mut durations_us: Vec<f64>) -> BenchOp {
    let n = durations_us.len().max(1);
    let total_us: f64 = durations_us.iter().sum();
    durations_us.sort_by(|a, b| a.partial_cmp(b).unwrap_or(std::cmp::Ordering::Equal));
    let pct = |q: f64| -> f64 {
        if durations_us.is_empty() {
            return 0.0;
        }
        let idx = (((durations_us.len() - 1) as f64) * q).round() as usize;
        durations_us[idx]
    };
    BenchOp {
        op: op.to_string(),
        iterations: durations_us.len(),
        total_ms: total_us / 1000.0,
        ops_per_sec: if total_us > 0.0 {
            n as f64 / (total_us / 1_000_000.0)
        } else {
            0.0
        },
        mean_us: total_us / n as f64,
        p50_us: pct(0.5),
        p99_us: pct(0.99),
    }
}

/// Run the goal/swarm benchmark suite against `scratch` (which the caller
/// creates and removes). Returns timing for each operation.
pub fn run(iterations: usize, scratch: &Path) -> Result<BenchReport> {
    let iterations = iterations.max(1);
    let ws = "bench";
    let mut ops = Vec::new();

    // goal_create: each a distinct durable record.
    let mut create_us = Vec::with_capacity(iterations);
    for i in 0..iterations {
        let req = GoalCreateRequest {
            title: format!("Bench goal {i}"),
            objective: format!("benchmark objective number {i} with sufficient substance"),
            verification_commands: vec!["cargo test".to_string()],
            ..Default::default()
        };
        let t = Instant::now();
        goalruntime::create_goal(scratch, ws, &req)?;
        create_us.push(t.elapsed().as_nanos() as f64 / 1000.0);
    }
    ops.push(summarize("goal_create", create_us));

    // A dedicated goal for the iterate/ledger/gate paths.
    let target = goalruntime::create_goal(
        scratch,
        ws,
        &GoalCreateRequest {
            title: "Bench target".to_string(),
            objective: "the goal iterate/ledger/gate paths run against this record".to_string(),
            allowed_surface: vec!["rust-core/src".to_string()],
            ..Default::default()
        },
    )?
    .0
    .goal_id;

    // goal_iterate: append (load + save grows O(n)).
    let mut iter_us = Vec::with_capacity(iterations);
    for i in 0..iterations {
        let req = GoalIterationAppendRequest {
            role: "builder".to_string(),
            summary: format!("benchmark iteration {i} doing measurable work"),
            evidence: vec![GoalEvidence {
                kind: "note".to_string(),
                notes: "bench".to_string(),
                ..Default::default()
            }],
            ..Default::default()
        };
        let t = Instant::now();
        swarm::record_lane(scratch, ws, &target, swarm::SwarmRole::Builder, req)?;
        iter_us.push(t.elapsed().as_nanos() as f64 / 1000.0);
    }
    ops.push(summarize("goal_iterate", iter_us));

    // goal_list: read + sort the whole workspace.
    let mut list_us = Vec::with_capacity(iterations);
    for _ in 0..iterations {
        let t = Instant::now();
        let _ = goalruntime::list_goals(scratch, ws)?;
        list_us.push(t.elapsed().as_nanos() as f64 / 1000.0);
    }
    ops.push(summarize("goal_list", list_us));

    // ledger_render: regenerate the markdown projection.
    let target_goal = goalruntime::get_goal(scratch, ws, &target)?;
    let mut ledger_us = Vec::with_capacity(iterations);
    for _ in 0..iterations {
        let t = Instant::now();
        goalruntime::write_ledger(scratch, ws, &target_goal)?;
        ledger_us.push(t.elapsed().as_nanos() as f64 / 1000.0);
    }
    ops.push(summarize("ledger_render", ledger_us));

    // swarm_gate: controller decision over all iterations.
    let mut gate_us = Vec::with_capacity(iterations);
    for _ in 0..iterations {
        let t = Instant::now();
        let _ = swarm::gate(scratch, ws, &target)?;
        gate_us.push(t.elapsed().as_nanos() as f64 / 1000.0);
    }
    ops.push(summarize("swarm_gate", gate_us));

    Ok(BenchReport { iterations, ops })
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn summarize_computes_percentiles() {
        let op = summarize("x", vec![1.0, 2.0, 3.0, 4.0, 100.0]);
        assert_eq!(op.iterations, 5);
        assert!(op.p99_us >= op.p50_us);
        assert!(op.ops_per_sec > 0.0);
        assert!(op.mean_us > 0.0);
    }

    #[test]
    fn bench_runs_all_ops() {
        let dir = TempDir::new().unwrap();
        let report = run(8, dir.path()).unwrap();
        let names: Vec<&str> = report.ops.iter().map(|o| o.op.as_str()).collect();
        for expected in [
            "goal_create",
            "goal_iterate",
            "goal_list",
            "ledger_render",
            "swarm_gate",
        ] {
            assert!(names.contains(&expected), "missing {expected}");
        }
        assert!(report.ops.iter().all(|o| o.iterations == 8));
    }
}
