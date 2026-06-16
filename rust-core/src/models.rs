use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WorkspaceRecord {
    pub workspace_id: String,
    pub name: String,
    pub root_path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest_scan_at: Option<String>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WorkspaceSnapshot {
    #[serde(default)]
    pub scanner_version: i64,
    pub workspace: WorkspaceRecord,
    pub summary: serde_json::Value,
    pub issues: Vec<IssueRecord>,
    pub signals: Vec<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<serde_json::Value>,
    #[serde(default)]
    pub drift_summary: serde_json::Value,
    pub runtimes: Vec<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_targets: Vec<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verify_targets: Vec<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub project_info: Option<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest_ledger: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest_verdicts: Option<String>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AppSettings {
    #[serde(default)]
    pub local_agent_type: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub codex_bin: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub opencode_bin: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub codex_args: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub codex_model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub opencode_model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub postgres_dsn: Option<String>,
    #[serde(default)]
    pub postgres_schema: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueRecord {
    pub bug_id: String,
    pub title: String,
    pub severity: String,
    #[serde(default)]
    pub issue_status: String,
    #[serde(default)]
    pub source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_doc: Option<String>,
    #[serde(default)]
    pub doc_status: String,
    #[serde(default)]
    pub code_status: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub summary: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub impact: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_evidence: Vec<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests_added: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests_passed: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub drift_flags: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub verified_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub verified_by: Option<String>,
    #[serde(default)]
    pub needs_followup: bool,
    #[serde(default)]
    pub review_ready_count: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub review_ready_runs: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fingerprint: Option<String>,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunRecord {
    pub run_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub runtime: String,
    pub model: String,
    pub status: String,
    pub title: String,
    pub prompt: String,
    pub command: Vec<String>,
    pub command_preview: String,
    pub log_path: String,
    pub output_path: String,
    #[serde(default)]
    pub created_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub started_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub completed_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pid: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runbook_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub eval_scenario_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub eval_replay_batch_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub worktree: Option<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub summary: Option<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub plan: Option<serde_json::Value>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FixRecord {
    pub fix_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    #[serde(default)]
    pub status: String,
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub how: Option<String>,
    pub actor: serde_json::Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub session_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub changed_files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests_run: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub worktree: Option<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default)]
    pub updated_at: String,
    #[serde(default)]
    pub recorded_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationRecord {
    pub verification_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub run_id: String,
    pub runtime: String,
    pub model: String,
    #[serde(default)]
    pub code_checked: String,
    #[serde(default)]
    pub fixed: String,
    #[serde(default)]
    pub confidence: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests: Vec<String>,
    pub actor: serde_json::Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub raw_excerpt: Option<String>,
    #[serde(default)]
    pub created_at: String,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn workspace_record_round_trip() {
        let record = WorkspaceRecord {
            workspace_id: "ws-1".into(),
            name: "test-workspace".into(),
            root_path: "/tmp/test".into(),
            latest_scan_at: Some("2026-01-01T00:00:00Z".into()),
            created_at: "2026-01-01T00:00:00Z".into(),
            updated_at: "2026-01-01T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&record).unwrap();
        let deserialized: WorkspaceRecord = serde_json::from_str(&json).unwrap();
        assert_eq!(record, deserialized);
    }

    #[test]
    fn app_settings_round_trip() {
        let settings = AppSettings {
            local_agent_type: "opencode".into(),
            codex_bin: Some("/usr/bin/codex".into()),
            opencode_bin: None,
            codex_args: None,
            codex_model: Some("deepseek-v4-pro".into()),
            opencode_model: None,
            postgres_dsn: Some("postgresql://localhost/xmustard".into()),
            postgres_schema: "xmustard".into(),
        };
        let json = serde_json::to_string(&settings).unwrap();
        let deserialized: AppSettings = serde_json::from_str(&json).unwrap();
        assert_eq!(settings, deserialized);
    }

    #[test]
    fn issue_record_round_trip() {
        let record = IssueRecord {
            bug_id: "BUG-001".into(),
            title: "Null pointer in parser".into(),
            severity: "high".into(),
            issue_status: "open".into(),
            source: "ledger".into(),
            source_doc: Some("docs/bugs/BUG-001.md".into()),
            doc_status: "open".into(),
            code_status: "unknown".into(),
            summary: Some("A null pointer dereference in the parser".into()),
            impact: Some("Crashes the service on malformed input".into()),
            evidence: vec![serde_json::json!({
                "path": "src/parser.rs",
                "line": 42,
                "excerpt": "let x = *ptr;"
            })],
            verification_evidence: vec![],
            tests_added: vec!["test_parser_null".into()],
            tests_passed: vec!["test_parser_null".into()],
            drift_flags: vec![],
            labels: vec!["bug".into(), "parser".into()],
            notes: Some("Found during fuzzing".into()),
            verified_at: None,
            verified_by: None,
            needs_followup: true,
            review_ready_count: 1,
            review_ready_runs: vec!["run-1".into()],
            fingerprint: Some("abc123".into()),
            updated_at: "2026-01-01T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&record).unwrap();
        let deserialized: IssueRecord = serde_json::from_str(&json).unwrap();
        assert_eq!(record, deserialized);
    }

    #[test]
    fn issue_record_minimal_deserialization() {
        let json = r#"{"bug_id":"BUG-002","title":"Minimal bug","severity":"low"}"#;
        let issue: IssueRecord = serde_json::from_str(json).unwrap();
        assert_eq!(issue.bug_id, "BUG-002");
        assert_eq!(issue.title, "Minimal bug");
        assert_eq!(issue.severity, "low");
        assert_eq!(issue.issue_status, "");
        assert_eq!(issue.source, "");
        assert!(issue.source_doc.is_none());
        assert_eq!(issue.doc_status, "");
        assert_eq!(issue.code_status, "");
        assert!(issue.summary.is_none());
        assert!(issue.impact.is_none());
        assert!(issue.evidence.is_empty());
        assert!(issue.verification_evidence.is_empty());
        assert!(issue.tests_added.is_empty());
        assert!(issue.tests_passed.is_empty());
        assert!(issue.drift_flags.is_empty());
        assert!(issue.labels.is_empty());
        assert!(issue.notes.is_none());
        assert!(issue.verified_at.is_none());
        assert!(issue.verified_by.is_none());
        assert!(!issue.needs_followup);
        assert_eq!(issue.review_ready_count, 0);
        assert!(issue.review_ready_runs.is_empty());
        assert!(issue.fingerprint.is_none());
        assert_eq!(issue.updated_at, "");
    }
}
