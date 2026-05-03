use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct SubsystemBoundary {
    pub name: &'static str,
    pub current_owner: &'static str,
    pub target_owner: &'static str,
    pub notes: &'static str,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct AgentProtocol {
    pub protocol_id: &'static str,
    pub transport: &'static str,
    pub direction: &'static str,
    pub owned_by: &'static str,
    pub purpose: &'static str,
    pub payloads: Vec<&'static str>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct AgentSurface {
    pub surface_id: &'static str,
    pub title: &'static str,
    pub product_promise: &'static str,
    pub control_plane_owner: &'static str,
    pub core_owner: &'static str,
    pub steady_state_budget_mb: u16,
    pub protocols: Vec<AgentProtocol>,
    pub durable_artifacts: Vec<&'static str>,
    pub example_endpoints: Vec<&'static str>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct PythonBoundaryCutline {
    pub boundary_id: &'static str,
    pub current_owner: &'static str,
    pub target_owner: &'static str,
    pub rust_role: &'static str,
    pub python_modules: Vec<&'static str>,
    pub why_next: &'static str,
    pub first_slice: &'static str,
    pub removable_when: Vec<&'static str>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct DiagnosticContract {
    pub contract_id: &'static str,
    pub owner: &'static str,
    pub delivery_owner: &'static str,
    pub durable_store: &'static str,
    pub required_fields: Vec<&'static str>,
    pub normalized_severities: Vec<&'static str>,
    pub source_kinds: Vec<&'static str>,
    pub notes: &'static str,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ArchitectureContract {
    pub design_version: &'static str,
    pub steady_state_runtime_budget_mb: u16,
    pub control_plane_owner: &'static str,
    pub core_owner: &'static str,
    pub python_end_state: &'static str,
    pub boundaries: Vec<SubsystemBoundary>,
    pub agent_surfaces: Vec<AgentSurface>,
    pub diagnostics_contract: DiagnosticContract,
    pub next_removable_python_boundary: PythonBoundaryCutline,
}

pub fn planned_boundaries() -> Vec<SubsystemBoundary> {
    vec![
        SubsystemBoundary {
            name: "scanner",
            current_owner: "python",
            target_owner: "rust-core",
            notes: "Own repo scanning, signal extraction, ledger ingestion, and verdict merging.",
        },
        SubsystemBoundary {
            name: "repo_map",
            current_owner: "python",
            target_owner: "rust-core",
            notes: "Own structural repo summaries and ranked path retrieval.",
        },
        SubsystemBoundary {
            name: "verification",
            current_owner: "python",
            target_owner: "rust-core",
            notes: "Own verification process control and artifact parsing.",
        },
        SubsystemBoundary {
            name: "retrieval_store",
            current_owner: "python",
            target_owner: "rust-core",
            notes: "Own repo-map ranking, evidence retrieval, and store-critical artifact helpers behind stable contracts.",
        },
        SubsystemBoundary {
            name: "http_api",
            current_owner: "python",
            target_owner: "api-go",
            notes: "Own HTTP routing and route-by-route parity with the existing frontend contract.",
        },
        SubsystemBoundary {
            name: "agent_protocols",
            current_owner: "python + mixed",
            target_owner: "api-go + rust-core",
            notes: "Own explicit plugin manifests, context packet protocols, and command-agent contracts for external tools.",
        },
    ]
}

pub fn planned_agent_surfaces() -> Vec<AgentSurface> {
    vec![
        AgentSurface {
            surface_id: "works_with_agents",
            title: "Works With Agents",
            product_promise: "xMustard exchanges durable issue, run, and review state with external agents and chat apps without surrendering evidence ownership.",
            control_plane_owner: "api-go",
            core_owner: "rust-core",
            steady_state_budget_mb: 96,
            protocols: vec![
                AgentProtocol {
                    protocol_id: "agent_plugin_manifest_v1",
                    transport: "https/json",
                    direction: "bidirectional",
                    owned_by: "api-go",
                    purpose: "Declare plugin identity, auth mode, subscribed topics, and callback URLs for external agents or chat apps.",
                    payloads: vec!["plugin_manifest", "subscription_topics", "auth_config"],
                },
                AgentProtocol {
                    protocol_id: "agent_event_sink_v1",
                    transport: "https/webhook",
                    direction: "outbound",
                    owned_by: "api-go",
                    purpose: "Push run, review, verification, and queue events to external systems using durable IDs instead of chat-thread state.",
                    payloads: vec!["run_event", "review_event", "verification_event"],
                },
                AgentProtocol {
                    protocol_id: "review_bundle_v1",
                    transport: "json bundle",
                    direction: "outbound",
                    owned_by: "rust-core",
                    purpose: "Export issue, fix, verification, and evidence bundles for outside review or audit agents.",
                    payloads: vec!["issue_bundle", "run_bundle", "verification_bundle"],
                },
            ],
            durable_artifacts: vec![
                "export_bundle.json",
                "run_insights.json",
                "review_queue.json",
                "guidance_paths",
            ],
            example_endpoints: vec![
                "/api/agent/surfaces",
                "/api/workspaces/{workspace_id}/export",
                "/api/workspaces/{workspace_id}/runs/{run_id}/insights",
            ],
        },
        AgentSurface {
            surface_id: "works_within_agents",
            title: "Works Within Agents",
            product_promise: "xMustard can be embedded inside agent sessions through repo-native guidance, structured context, and replayable evidence packets.",
            control_plane_owner: "api-go",
            core_owner: "rust-core",
            steady_state_budget_mb: 160,
            protocols: vec![
                AgentProtocol {
                    protocol_id: "repo_guidance_packet_v1",
                    transport: "markdown+yaml",
                    direction: "inbound",
                    owned_by: "api-go",
                    purpose: "Expose AGENTS.md, .xmustard.yaml path instructions, and MCP/browser hints as inspectable repo guidance.",
                    payloads: vec!["guidance_record", "path_instruction", "mcp_hint"],
                },
                AgentProtocol {
                    protocol_id: "issue_context_packet_v1",
                    transport: "json",
                    direction: "outbound",
                    owned_by: "api-go + rust-core",
                    purpose: "Deliver issue facts, evidence, repo-map retrieval, runbooks, and verification profiles into an agent session.",
                    payloads: vec!["issue_context_packet", "repo_map_summary", "related_artifacts"],
                },
                AgentProtocol {
                    protocol_id: "context_replay_packet_v1",
                    transport: "json",
                    direction: "bidirectional",
                    owned_by: "api-go + rust-core",
                    purpose: "Replay saved context variants and eval packets so agent behavior can be compared over durable evidence.",
                    payloads: vec!["context_replay", "eval_scenario", "comparison_report"],
                },
            ],
            durable_artifacts: vec![
                "AGENTS.md",
                ".xmustard.yaml",
                "context_replays.json",
                "browser_dumps.json",
                "eval_scenarios.json",
            ],
            example_endpoints: vec![
                "/api/workspaces/{workspace_id}/issues/{issue_id}/context",
                "/api/workspaces/{workspace_id}/issues/{issue_id}/work",
                "/api/workspaces/{workspace_id}/issues/{issue_id}/context-replays",
            ],
        },
        AgentSurface {
            surface_id: "commands_agents",
            title: "Commands Agents",
            product_promise: "xMustard launches, plans, gates, and audits agent work as a governed issue-first control plane.",
            control_plane_owner: "api-go",
            core_owner: "rust-core",
            steady_state_budget_mb: 224,
            protocols: vec![
                AgentProtocol {
                    protocol_id: "agent_run_request_v1",
                    transport: "https/json",
                    direction: "outbound",
                    owned_by: "api-go",
                    purpose: "Create issue runs and workspace queries with runtime, model, runbook, and evidence references.",
                    payloads: vec!["run_request", "workspace_query", "runtime_selection"],
                },
                AgentProtocol {
                    protocol_id: "agent_plan_gate_v1",
                    transport: "https/json",
                    direction: "bidirectional",
                    owned_by: "api-go",
                    purpose: "Approve, reject, and modify execution plans before the runtime commits code or verification work.",
                    payloads: vec!["run_plan", "plan_approval", "plan_rejection"],
                },
                AgentProtocol {
                    protocol_id: "run_event_stream_v1",
                    transport: "terminal+json",
                    direction: "bidirectional",
                    owned_by: "rust-core",
                    purpose: "Run process control, log streaming, and durable execution artifacts through a systems-safe runtime boundary.",
                    payloads: vec!["run_log", "terminal_chunk", "run_metrics", "verification_result"],
                },
            ],
            durable_artifacts: vec![
                "runs/*.json",
                "plans",
                "critique",
                "fixes",
                "verifications",
            ],
            example_endpoints: vec![
                "/api/workspaces/{workspace_id}/issues/{issue_id}/runs",
                "/api/workspaces/{workspace_id}/agent/query",
                "/api/workspaces/{workspace_id}/runs/{run_id}/plan",
                "/api/terminal/open",
            ],
        },
    ]
}

pub fn next_removable_python_boundary() -> PythonBoundaryCutline {
    PythonBoundaryCutline {
        boundary_id: "runtime_and_terminal_process_plane",
        current_owner: "backend/app/runtimes.py + backend/app/terminal.py + backend/app/service.py",
        target_owner: "api-go terminal/runtime shell + rust-core managed process runner",
        rust_role: "Own process-safe execution, bounded output capture, timeout/cancellation, and structured run summaries for long-lived agent and verification work.",
        python_modules: vec!["backend/app/runtimes.py", "backend/app/terminal.py", "backend/app/service.py"],
        why_next: "Python is no longer the external integrations gateway on the live request path, Python compatibility runtime discovery/probe delegates to Go, and Go now covers runtime argument parsing plus PTY terminal fidelity. The remaining gap is managed execution and run summarization, which still duplicate subprocess control across Python and Go.",
        first_slice: "Move managed execution, bounded buffering, timeout/cancellation, and structured run summarization behind a Rust command boundary instead of continuing to duplicate long-lived subprocess control in Python and Go.",
        removable_when: vec![
            "Rust owns the managed process runner for long-lived execution, bounded buffering, timeout, cancellation, and structured summaries.",
            "Go delegates managed runs to the Rust command boundary instead of keeping duplicate subprocess control in run_control.go.",
            "Python no longer handles runtime launch, probe, query, or terminal request paths.",
        ],
    }
}

pub fn diagnostics_contract_v1() -> DiagnosticContract {
    DiagnosticContract {
        contract_id: "diagnostics.normalized.v1",
        owner: "rust-core",
        delivery_owner: "api-go",
        durable_store: "postgres.diagnostics",
        required_fields: vec![
            "workspace_id",
            "path",
            "range_start_line",
            "range_start_column",
            "range_end_line",
            "range_end_column",
            "severity",
            "message",
            "source_kind",
            "source_name",
            "rule_code",
            "fingerprint",
            "generated_at",
        ],
        normalized_severities: vec!["error", "warning", "info", "hint"],
        source_kinds: vec!["lsp", "compiler", "test", "scanner", "manual"],
        notes: "Rust normalizes diagnostic meaning from LSP/compiler/scanner inputs; Go delivers it; Postgres persists baselines and replayable rows. Python must not become the first LSP diagnostics owner.",
    }
}

pub fn no_python_architecture_contract() -> ArchitectureContract {
    ArchitectureContract {
        design_version: "2026-04-18.no-python-control-plane",
        steady_state_runtime_budget_mb: 500,
        control_plane_owner: "api-go",
        core_owner: "rust-core",
        python_end_state: "No Python service remains on the steady-state request path; Python is tolerated only as temporary migration tooling until deleted.",
        boundaries: planned_boundaries(),
        agent_surfaces: planned_agent_surfaces(),
        diagnostics_contract: diagnostics_contract_v1(),
        next_removable_python_boundary: next_removable_python_boundary(),
    }
}

#[cfg(test)]
mod tests {
    use super::{
        diagnostics_contract_v1, next_removable_python_boundary, no_python_architecture_contract,
        planned_boundaries,
    };

    #[test]
    fn boundaries_cover_core_migration_targets() {
        let boundaries = planned_boundaries();
        assert!(boundaries.iter().any(|item| item.name == "scanner"));
        assert!(boundaries.iter().any(|item| item.name == "http_api"));
        assert!(boundaries.iter().any(|item| item.name == "agent_protocols"));
    }

    #[test]
    fn architecture_contract_exposes_three_agent_surfaces() {
        let contract = no_python_architecture_contract();
        assert_eq!(contract.agent_surfaces.len(), 3);
        assert_eq!(contract.steady_state_runtime_budget_mb, 500);
        assert!(
            contract
                .agent_surfaces
                .iter()
                .any(|item| item.surface_id == "works_with_agents")
        );
        assert!(
            contract
                .agent_surfaces
                .iter()
                .any(|item| item.surface_id == "works_within_agents")
        );
        assert!(
            contract
                .agent_surfaces
                .iter()
                .any(|item| item.surface_id == "commands_agents")
        );
    }

    #[test]
    fn next_python_cutline_targets_runtime_and_terminal_plane() {
        let cutline = next_removable_python_boundary();
        assert_eq!(cutline.boundary_id, "runtime_and_terminal_process_plane");
        assert_eq!(cutline.target_owner, "api-go terminal/runtime shell + rust-core managed process runner");
        assert!(cutline.why_next.contains("Go now covers runtime argument parsing plus PTY terminal fidelity"));
        assert!(cutline.first_slice.contains("Move managed execution"));
    }

    #[test]
    fn diagnostics_contract_sets_lsp_normalized_boundary() {
        let contract = diagnostics_contract_v1();
        assert_eq!(contract.contract_id, "diagnostics.normalized.v1");
        assert_eq!(contract.owner, "rust-core");
        assert_eq!(contract.delivery_owner, "api-go");
        assert_eq!(contract.durable_store, "postgres.diagnostics");
        assert!(contract.required_fields.contains(&"fingerprint"));
        assert!(contract.normalized_severities.contains(&"warning"));
        assert!(contract.source_kinds.contains(&"lsp"));
    }
}
