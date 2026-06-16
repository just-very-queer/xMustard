//! Goal runtime: a durable, evidence-gated objective layer for a workspace.
//!
//! This is the Rust owner of the `/goal` data contract described in
//! `docs/GOAL_RUNTIME.md`. It is wire-compatible with the Go shell in
//! `api-go/internal/workspaceops/goals.go`: it reads and writes the same
//! `goals.json`, `goal_iterations/<goal_id>.json`, and `goals/<goal_id>.md`
//! files, so either runtime can own the surface.
//!
//! Design guarantees:
//! * Memory safe: no `unsafe`, no unbounded buffering, atomic file writes.
//! * Type safe: status is a closed enum, every operation returns `Result`.
//! * Anti-AI-slop guardrails: create/iterate refuse blocking slop, and
//!   completion is gated on verifiable evidence (see [`slop`]).

use chrono::{SecondsFormat, Utc};
use serde::{Deserialize, Serialize};
use sha1::{Digest, Sha1};
use std::collections::BTreeSet;
use std::fs;
use std::path::{Path, PathBuf};
use thiserror::Error;

/// Result alias for goal runtime operations.
pub type Result<T> = std::result::Result<T, GoalError>;

/// Errors surfaced by the goal runtime. Every variant carries enough context
/// to be actionable from a one-shot CLI invocation.
#[derive(Debug, Error)]
pub enum GoalError {
    #[error("io error at {path}: {source}")]
    Io {
        path: String,
        #[source]
        source: std::io::Error,
    },
    #[error("json error at {path}: {source}")]
    Json {
        path: String,
        #[source]
        source: serde_json::Error,
    },
    #[error("goal not found: {0}")]
    NotFound(String),
    #[error("validation failed: {0}")]
    Validation(String),
    /// Returned when a `complete` transition is refused by the completion gate
    /// or by the anti-slop guardrails. The message lists the blocking findings.
    #[error("completion blocked: {0}")]
    CompletionBlocked(String),
    /// Returned when create/iterate input trips a blocking anti-slop rule.
    #[error("slop guardrail blocked the operation: {0}")]
    SlopBlocked(String),
}

/// Closed set of goal lifecycle states. Serializes to the same lowercase
/// strings the Go shell writes (`draft`, `active`, ...).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum GoalStatus {
    Draft,
    Active,
    Blocked,
    Complete,
    Archived,
}

impl GoalStatus {
    pub fn as_str(self) -> &'static str {
        match self {
            GoalStatus::Draft => "draft",
            GoalStatus::Active => "active",
            GoalStatus::Blocked => "blocked",
            GoalStatus::Complete => "complete",
            GoalStatus::Archived => "archived",
        }
    }

    /// Parse a status string the way the Go shell does: trimmed and lowercased.
    pub fn parse(value: &str) -> Result<Self> {
        match value.trim().to_ascii_lowercase().as_str() {
            "draft" => Ok(GoalStatus::Draft),
            "active" => Ok(GoalStatus::Active),
            "blocked" => Ok(GoalStatus::Blocked),
            "complete" => Ok(GoalStatus::Complete),
            "archived" => Ok(GoalStatus::Archived),
            other => Err(GoalError::Validation(format!(
                "invalid goal status {other:?}"
            ))),
        }
    }
}

/// A single piece of evidence attached to a goal or iteration. Field tags match
/// the Go `GoalEvidence` struct so the JSON round-trips both ways.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct GoalEvidence {
    pub kind: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub label: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub command: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub outcome: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub url: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub notes: String,
    /// Always serialized; tolerated as absent on incoming request evidence
    /// because the runtime stamps it in `normalize_evidence`.
    #[serde(default)]
    pub created_at: String,
}

impl GoalEvidence {
    /// True when this evidence is durable proof of a successful check, matching
    /// `isVerificationEvidence` in the Go shell. Either an explicit verification
    /// kind, or a command paired with a passing outcome.
    pub fn is_verification(&self) -> bool {
        let kind = self.kind.trim().to_ascii_lowercase();
        let outcome = self.outcome.trim().to_ascii_lowercase();
        if matches!(kind.as_str(), "verification" | "test" | "build" | "lint") {
            return true;
        }
        if !self.command.trim().is_empty()
            && matches!(outcome.as_str(), "pass" | "passed" | "success" | "ok")
        {
            return true;
        }
        false
    }
}

/// One recorded worker turn against a goal.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct GoalIterationRecord {
    pub iteration_id: String,
    pub goal_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub role: String,
    pub summary: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub outcome: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub runtime: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub model: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub files_touched: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<GoalEvidence>,
    pub created_at: String,
}

/// The durable goal record.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct GoalRecord {
    pub goal_id: String,
    pub workspace_id: String,
    pub title: String,
    pub objective: String,
    pub status: GoalStatus,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub acceptance_criteria: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub current_tranche: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub allowed_surface: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_commands: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profile_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub runtime_preference: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub preferred_model: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub resumption_notes: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<GoalEvidence>,
    pub created_at: String,
    pub updated_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub completed_at: Option<String>,
}

/// Input for creating a goal. Free of identity/time fields; the runtime owns
/// those so they cannot be forged by a worker.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct GoalCreateRequest {
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub objective: String,
    #[serde(default)]
    pub acceptance_criteria: Vec<String>,
    #[serde(default)]
    pub current_tranche: String,
    #[serde(default)]
    pub allowed_surface: Vec<String>,
    #[serde(default)]
    pub verification_commands: Vec<String>,
    #[serde(default)]
    pub verification_profile_ids: Vec<String>,
    #[serde(default)]
    pub runtime_preference: String,
    #[serde(default)]
    pub preferred_model: String,
    #[serde(default)]
    pub resumption_notes: String,
}

/// Input for appending an iteration.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct GoalIterationAppendRequest {
    #[serde(default)]
    pub role: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub outcome: String,
    #[serde(default)]
    pub runtime: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub files_touched: Vec<String>,
    #[serde(default)]
    pub evidence: Vec<GoalEvidence>,
}

// ---------------------------------------------------------------------------
// Anti-AI-slop guardrails
// ---------------------------------------------------------------------------

/// Anti-slop linter: deterministic checks that refuse fabricated, empty, or
/// unverifiable goal/iteration content. This is the guardrail layer that keeps
/// generated output honest before it becomes durable evidence.
pub mod slop {
    use super::{GoalEvidence, GoalIterationAppendRequest, GoalRecord, GoalStatus};
    use serde::Serialize;

    #[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
    #[serde(rename_all = "lowercase")]
    pub enum SlopSeverity {
        /// Recorded for transparency; does not block.
        Info,
        /// Allowed but surfaced loudly; the content is suspicious.
        Warning,
        /// Refused: the operation must not persist this content.
        Blocking,
    }

    #[derive(Debug, Clone, PartialEq, Eq, Serialize)]
    pub struct SlopFinding {
        pub code: &'static str,
        pub severity: SlopSeverity,
        pub field: String,
        pub message: String,
    }

    #[derive(Debug, Clone, PartialEq, Eq, Serialize)]
    pub struct SlopReport {
        pub ok: bool,
        pub blocking: usize,
        pub warnings: usize,
        pub findings: Vec<SlopFinding>,
    }

    impl SlopReport {
        fn from_findings(findings: Vec<SlopFinding>) -> Self {
            let blocking = findings
                .iter()
                .filter(|f| f.severity == SlopSeverity::Blocking)
                .count();
            let warnings = findings
                .iter()
                .filter(|f| f.severity == SlopSeverity::Warning)
                .count();
            SlopReport {
                ok: blocking == 0,
                blocking,
                warnings,
                findings,
            }
        }

        /// One-line summary of blocking findings, for error messages.
        pub fn blocking_summary(&self) -> String {
            self.findings
                .iter()
                .filter(|f| f.severity == SlopSeverity::Blocking)
                .map(|f| format!("[{}] {}", f.code, f.message))
                .collect::<Vec<_>>()
                .join("; ")
        }
    }

    /// Markers that a worker bailed instead of doing the work. Recording these
    /// as a real iteration/objective would launder a non-result into evidence.
    const REFUSAL_MARKERS: &[&str] = &[
        "as an ai",
        "as a language model",
        "i cannot",
        "i can not",
        "i can't",
        "i am unable",
        "i'm unable",
        "i am sorry",
        "i'm sorry",
        "unable to assist",
        "cannot assist with",
    ];

    /// Markers of placeholder / unfinished output. Allowed, but flagged so the
    /// controller never mistakes filler for finished work.
    const PLACEHOLDER_MARKERS: &[&str] = &[
        "lorem ipsum",
        "placeholder",
        "tbd",
        "to be determined",
        "todo",
        "fixme",
        "your code here",
        "insert code here",
        "<insert",
        "rest of the code",
        "remaining code here",
        "code goes here",
        "implement this",
        "and so on",
        "...etc",
        "etc etc",
    ];

    const MIN_SUMMARY_CHARS: usize = 12;

    fn contains_marker(haystack: &str, markers: &[&'static str]) -> Option<&'static str> {
        let lower = haystack.to_ascii_lowercase();
        markers.iter().copied().find(|m| lower.contains(*m))
    }

    /// Lint a required free-text field (objective, summary). `required` makes an
    /// empty value a blocking finding.
    fn lint_text(field: &str, value: &str, required: bool, findings: &mut Vec<SlopFinding>) {
        let trimmed = value.trim();
        if trimmed.is_empty() {
            if required {
                findings.push(SlopFinding {
                    code: "empty-required",
                    severity: SlopSeverity::Blocking,
                    field: field.to_string(),
                    message: format!("{field} is required but empty or whitespace"),
                });
            }
            return;
        }
        // Content that is only punctuation/ellipsis is non-information.
        if trimmed.chars().all(|c| !c.is_alphanumeric()) {
            findings.push(SlopFinding {
                code: "no-information",
                severity: SlopSeverity::Blocking,
                field: field.to_string(),
                message: format!("{field} contains no alphanumeric content"),
            });
            return;
        }
        if let Some(marker) = contains_marker(trimmed, REFUSAL_MARKERS) {
            findings.push(SlopFinding {
                code: "refusal-marker",
                severity: SlopSeverity::Blocking,
                field: field.to_string(),
                message: format!("{field} reads as a refusal/non-result ({marker:?})"),
            });
        }
        if let Some(marker) = contains_marker(trimmed, PLACEHOLDER_MARKERS) {
            findings.push(SlopFinding {
                code: "placeholder-marker",
                severity: SlopSeverity::Warning,
                field: field.to_string(),
                message: format!("{field} contains placeholder text ({marker:?})"),
            });
        }
    }

    /// Lint a single evidence item: a success claim that nothing can verify is
    /// the most common form of slop, so flag it.
    pub fn lint_evidence(field: &str, evidence: &GoalEvidence) -> Vec<SlopFinding> {
        let mut findings = Vec::new();
        let outcome = evidence.outcome.trim().to_ascii_lowercase();
        let claims_success =
            matches!(outcome.as_str(), "pass" | "passed" | "success" | "ok" | "green");
        let has_anchor = !evidence.command.trim().is_empty()
            || !evidence.path.trim().is_empty()
            || !evidence.url.trim().is_empty();
        if claims_success && !has_anchor {
            findings.push(SlopFinding {
                code: "unverifiable-success",
                severity: SlopSeverity::Warning,
                field: field.to_string(),
                message:
                    "evidence claims success but carries no command, path, or url to verify it"
                        .to_string(),
            });
        }
        if evidence.kind.trim().is_empty() {
            findings.push(SlopFinding {
                code: "evidence-untyped",
                severity: SlopSeverity::Info,
                field: field.to_string(),
                message: "evidence has no kind; defaulting to note".to_string(),
            });
        }
        findings
    }

    /// Lint the input to `create_goal`.
    pub fn lint_create(title: &str, objective: &str, acceptance: &[String]) -> SlopReport {
        let mut findings = Vec::new();
        lint_text("title", title, true, &mut findings);
        lint_text("objective", objective, true, &mut findings);
        if objective.trim().len() < MIN_SUMMARY_CHARS && !objective.trim().is_empty() {
            findings.push(SlopFinding {
                code: "objective-too-thin",
                severity: SlopSeverity::Warning,
                field: "objective".to_string(),
                message: format!("objective is under {MIN_SUMMARY_CHARS} characters"),
            });
        }
        for (idx, item) in acceptance.iter().enumerate() {
            lint_text(
                &format!("acceptance_criteria[{idx}]"),
                item,
                false,
                &mut findings,
            );
        }
        SlopReport::from_findings(findings)
    }

    /// Lint the input to `append_iteration`. `objective` is passed so we can
    /// catch the lazy pattern of echoing the objective back as a "summary".
    pub fn lint_iteration(req: &GoalIterationAppendRequest, objective: &str) -> SlopReport {
        let mut findings = Vec::new();
        lint_text("summary", &req.summary, true, &mut findings);
        let summary = req.summary.trim();
        if !summary.is_empty() && summary.chars().count() < MIN_SUMMARY_CHARS {
            findings.push(SlopFinding {
                code: "summary-too-thin",
                severity: SlopSeverity::Warning,
                field: "summary".to_string(),
                message: format!("summary is under {MIN_SUMMARY_CHARS} characters of substance"),
            });
        }
        if !summary.is_empty() && summary.eq_ignore_ascii_case(objective.trim()) {
            findings.push(SlopFinding {
                code: "summary-echoes-objective",
                severity: SlopSeverity::Warning,
                field: "summary".to_string(),
                message: "summary just restates the objective; no progress is described"
                    .to_string(),
            });
        }
        for (idx, ev) in req.evidence.iter().enumerate() {
            findings.extend(lint_evidence(&format!("evidence[{idx}]"), ev));
        }
        SlopReport::from_findings(findings)
    }

    /// The completion gate, expressed as a lint so the reasons are inspectable.
    /// Marking complete requires at least one verification evidence item across
    /// the goal record and its iterations.
    pub fn lint_completion(
        goal: &GoalRecord,
        iteration_evidence: &[&GoalEvidence],
        skip_reason: &str,
    ) -> SlopReport {
        let mut findings = Vec::new();
        let has_verification = goal.evidence.iter().any(GoalEvidence::is_verification)
            || iteration_evidence.iter().any(|e| e.is_verification());
        if goal.status == GoalStatus::Complete {
            // Already complete; nothing to gate.
            return SlopReport::from_findings(findings);
        }
        if !has_verification && skip_reason.trim().is_empty() {
            findings.push(SlopFinding {
                code: "complete-without-evidence",
                severity: SlopSeverity::Blocking,
                field: "status".to_string(),
                message: "cannot complete without verification evidence or a skip reason"
                    .to_string(),
            });
        }
        SlopReport::from_findings(findings)
    }
}

// ---------------------------------------------------------------------------
// Time + identity helpers (mirroring the Go shell)
// ---------------------------------------------------------------------------

fn now_utc() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Nanos, true)
}

/// Strip separators from a timestamp and keep the leading 15 chars, matching
/// `compactGoalTimestamp` in Go.
fn compact_timestamp(value: &str) -> String {
    let stripped: String = value
        .chars()
        .filter(|c| !matches!(c, '-' | ':' | '.' | 'Z'))
        .collect();
    stripped.chars().take(15).collect()
}

/// `slugProfileID` clone: lowercase, non-alnum runs to `-`, trimmed. Falls back
/// to a stable sha1-derived id for non-sluggable titles.
fn slug_id(value: &str) -> String {
    let lower = value.trim().to_ascii_lowercase();
    let mut slug = String::with_capacity(lower.len());
    let mut prev_dash = false;
    for ch in lower.chars() {
        if ch.is_ascii_alphanumeric() {
            slug.push(ch);
            prev_dash = false;
        } else if !prev_dash {
            slug.push('-');
            prev_dash = true;
        }
    }
    let slug = slug.trim_matches('-').to_string();
    if !slug.is_empty() {
        return slug;
    }
    let mut hasher = Sha1::new();
    hasher.update(value.as_bytes());
    let digest = hasher.finalize();
    let hex: String = digest.iter().map(|b| format!("{b:02x}")).collect();
    format!("goal-{}", &hex[..8])
}

fn unique_goal_id(existing: &[GoalRecord], title: &str, now: &str) -> String {
    let base = {
        let candidate = slug_id(title);
        if candidate.is_empty() {
            "goal".to_string()
        } else {
            candidate
        }
    };
    let seen: BTreeSet<&str> = existing.iter().map(|g| g.goal_id.as_str()).collect();
    if !seen.contains(base.as_str()) {
        return base;
    }
    let suffix = compact_timestamp(now);
    let with_suffix = format!("{base}-{suffix}");
    if !seen.contains(with_suffix.as_str()) {
        return with_suffix;
    }
    for i in 2.. {
        let candidate = format!("{base}-{suffix}-{i}");
        if !seen.contains(candidate.as_str()) {
            return candidate;
        }
    }
    unreachable!("an unbounded counter always finds a free id")
}

fn clean_string_slice(values: &[String]) -> Vec<String> {
    let mut out = Vec::new();
    let mut seen = BTreeSet::new();
    for value in values {
        let trimmed = value.trim();
        if trimmed.is_empty() {
            continue;
        }
        if seen.insert(trimmed.to_string()) {
            out.push(trimmed.to_string());
        }
    }
    out
}

fn normalize_runtime(runtime: &str) -> String {
    match runtime.trim().to_ascii_lowercase().as_str() {
        value @ ("codex" | "opencode" | "manual") => value.to_string(),
        _ => "manual".to_string(),
    }
}

fn fallback<'a>(value: &'a str, default: &'a str) -> &'a str {
    if value.trim().is_empty() {
        default
    } else {
        value
    }
}

// ---------------------------------------------------------------------------
// Store paths + atomic JSON IO
// ---------------------------------------------------------------------------

fn workspace_dir(data_dir: &Path, workspace_id: &str) -> PathBuf {
    data_dir.join("workspaces").join(workspace_id)
}

fn goals_path(data_dir: &Path, workspace_id: &str) -> PathBuf {
    workspace_dir(data_dir, workspace_id).join("goals.json")
}

fn goal_iterations_path(data_dir: &Path, workspace_id: &str, goal_id: &str) -> PathBuf {
    workspace_dir(data_dir, workspace_id)
        .join("goal_iterations")
        .join(format!("{goal_id}.json"))
}

fn goal_ledger_path(data_dir: &Path, workspace_id: &str, goal_id: &str) -> PathBuf {
    workspace_dir(data_dir, workspace_id)
        .join("goals")
        .join(format!("{goal_id}.md"))
}

fn io_err(path: &Path, source: std::io::Error) -> GoalError {
    GoalError::Io {
        path: path.display().to_string(),
        source,
    }
}

fn read_json<T: for<'de> Deserialize<'de> + Default>(path: &Path) -> Result<T> {
    match fs::read(path) {
        Ok(bytes) => serde_json::from_slice(&bytes).map_err(|source| GoalError::Json {
            path: path.display().to_string(),
            source,
        }),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(T::default()),
        Err(err) => Err(io_err(path, err)),
    }
}

/// Write JSON atomically: serialize, write a sibling temp file, fsync, rename.
/// A crash mid-write leaves the previous file intact rather than a truncated one.
fn write_json<T: Serialize>(path: &Path, payload: &T) -> Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| io_err(parent, e))?;
    }
    let body = serde_json::to_vec_pretty(payload).map_err(|source| GoalError::Json {
        path: path.display().to_string(),
        source,
    })?;
    write_bytes_atomic(path, &body)
}

fn write_bytes_atomic(path: &Path, body: &[u8]) -> Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| io_err(parent, e))?;
    }
    let tmp = path.with_file_name(format!(
        "{}.tmp.{}",
        path.file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_else(|| "goal".to_string()),
        std::process::id()
    ));
    {
        use std::io::Write;
        let mut file = fs::File::create(&tmp).map_err(|e| io_err(&tmp, e))?;
        file.write_all(body).map_err(|e| io_err(&tmp, e))?;
        file.sync_all().map_err(|e| io_err(&tmp, e))?;
    }
    fs::rename(&tmp, path).map_err(|e| io_err(path, e))
}

// ---------------------------------------------------------------------------
// Operations
// ---------------------------------------------------------------------------

fn load_goals(data_dir: &Path, workspace_id: &str) -> Result<Vec<GoalRecord>> {
    read_json(&goals_path(data_dir, workspace_id))
}

fn save_goals(data_dir: &Path, workspace_id: &str, goals: &[GoalRecord]) -> Result<()> {
    write_json(&goals_path(data_dir, workspace_id), &goals)
}

fn load_iterations(
    data_dir: &Path,
    workspace_id: &str,
    goal_id: &str,
) -> Result<Vec<GoalIterationRecord>> {
    read_json(&goal_iterations_path(data_dir, workspace_id, goal_id))
}

fn save_iterations(
    data_dir: &Path,
    workspace_id: &str,
    goal_id: &str,
    iterations: &[GoalIterationRecord],
) -> Result<()> {
    write_json(
        &goal_iterations_path(data_dir, workspace_id, goal_id),
        &iterations,
    )
}

/// List goals for a workspace, newest-updated first.
pub fn list_goals(data_dir: &Path, workspace_id: &str) -> Result<Vec<GoalRecord>> {
    let mut goals = load_goals(data_dir, workspace_id)?;
    goals.sort_by(|a, b| b.updated_at.cmp(&a.updated_at));
    Ok(goals)
}

/// List the recorded iterations for a goal, oldest first.
pub fn list_iterations(
    data_dir: &Path,
    workspace_id: &str,
    goal_id: &str,
) -> Result<Vec<GoalIterationRecord>> {
    load_iterations(data_dir, workspace_id, goal_id)
}

/// Fetch one goal by id.
pub fn get_goal(data_dir: &Path, workspace_id: &str, goal_id: &str) -> Result<GoalRecord> {
    load_goals(data_dir, workspace_id)?
        .into_iter()
        .find(|g| g.goal_id == goal_id)
        .ok_or_else(|| GoalError::NotFound(goal_id.to_string()))
}

/// Create a goal. Blocking anti-slop findings on the title/objective refuse the
/// create so junk objectives never become durable records.
pub fn create_goal(
    data_dir: &Path,
    workspace_id: &str,
    request: &GoalCreateRequest,
) -> Result<(GoalRecord, slop::SlopReport)> {
    let title = request.title.trim().to_string();
    let objective = request.objective.trim().to_string();
    if title.is_empty() {
        return Err(GoalError::Validation("goal title is required".into()));
    }
    if objective.is_empty() {
        return Err(GoalError::Validation("goal objective is required".into()));
    }
    let acceptance = clean_string_slice(&request.acceptance_criteria);
    let report = slop::lint_create(&title, &objective, &acceptance);
    if !report.ok {
        return Err(GoalError::SlopBlocked(report.blocking_summary()));
    }

    let mut existing = load_goals(data_dir, workspace_id)?;
    let now = now_utc();
    let goal_id = unique_goal_id(&existing, &title, &now);
    let goal = GoalRecord {
        goal_id: goal_id.clone(),
        workspace_id: workspace_id.to_string(),
        title,
        objective,
        status: GoalStatus::Draft,
        acceptance_criteria: acceptance,
        current_tranche: request.current_tranche.trim().to_string(),
        allowed_surface: clean_string_slice(&request.allowed_surface),
        verification_commands: clean_string_slice(&request.verification_commands),
        verification_profile_ids: clean_string_slice(&request.verification_profile_ids),
        runtime_preference: normalize_runtime(&request.runtime_preference),
        preferred_model: request.preferred_model.trim().to_string(),
        resumption_notes: request.resumption_notes.trim().to_string(),
        evidence: Vec::new(),
        created_at: now.clone(),
        updated_at: now,
        completed_at: None,
    };
    existing.push(goal.clone());
    save_goals(data_dir, workspace_id, &existing)?;
    save_iterations(data_dir, workspace_id, &goal_id, &[])?;
    write_ledger(data_dir, workspace_id, &goal)?;
    Ok((goal, report))
}

/// Append a worker iteration. Blocking anti-slop findings (empty/refusal
/// summary) refuse the append; warnings are returned for the caller to surface.
pub fn append_iteration(
    data_dir: &Path,
    workspace_id: &str,
    goal_id: &str,
    request: &GoalIterationAppendRequest,
) -> Result<(GoalIterationRecord, slop::SlopReport)> {
    let mut goals = load_goals(data_dir, workspace_id)?;
    let index = goals
        .iter()
        .position(|g| g.goal_id == goal_id)
        .ok_or_else(|| GoalError::NotFound(goal_id.to_string()))?;

    let report = slop::lint_iteration(request, &goals[index].objective);
    if !report.ok {
        return Err(GoalError::SlopBlocked(report.blocking_summary()));
    }

    let now = now_utc();
    let mut iterations = load_iterations(data_dir, workspace_id, goal_id)?;
    let evidence = normalize_evidence(&request.evidence, &now);
    let iteration = GoalIterationRecord {
        iteration_id: format!("{}-{:03}", compact_timestamp(&now), iterations.len() + 1),
        goal_id: goal_id.to_string(),
        role: request.role.trim().to_string(),
        summary: request.summary.trim().to_string(),
        outcome: request.outcome.trim().to_string(),
        runtime: request.runtime.trim().to_string(),
        model: request.model.trim().to_string(),
        files_touched: clean_string_slice(&request.files_touched),
        evidence: evidence.clone(),
        created_at: now.clone(),
    };
    iterations.push(iteration.clone());
    save_iterations(data_dir, workspace_id, goal_id, &iterations)?;

    let goal = &mut goals[index];
    goal.updated_at = now;
    goal.evidence.extend(evidence);
    let goal_snapshot = goal.clone();
    save_goals(data_dir, workspace_id, &goals)?;
    write_ledger(data_dir, workspace_id, &goal_snapshot)?;
    Ok((iteration, report))
}

/// Update goal status. A `complete` transition runs the completion gate: it
/// requires verification evidence or an explicit, recorded skip reason.
pub fn update_status(
    data_dir: &Path,
    workspace_id: &str,
    goal_id: &str,
    status: GoalStatus,
    skip_reason: &str,
) -> Result<GoalRecord> {
    let mut goals = load_goals(data_dir, workspace_id)?;
    let index = goals
        .iter()
        .position(|g| g.goal_id == goal_id)
        .ok_or_else(|| GoalError::NotFound(goal_id.to_string()))?;

    let now = now_utc();
    if status == GoalStatus::Complete {
        let iterations = load_iterations(data_dir, workspace_id, goal_id)?;
        let iteration_evidence: Vec<&GoalEvidence> =
            iterations.iter().flat_map(|i| i.evidence.iter()).collect();
        let report = slop::lint_completion(&goals[index], &iteration_evidence, skip_reason);
        if !report.ok {
            return Err(GoalError::CompletionBlocked(report.blocking_summary()));
        }
        let has_verification = goals[index].evidence.iter().any(GoalEvidence::is_verification)
            || iteration_evidence.iter().any(|e| e.is_verification());
        if !has_verification {
            // Skip reason was supplied; persist it as audit evidence.
            goals[index].evidence.push(GoalEvidence {
                kind: "verification-skip".to_string(),
                label: "Verification skipped by operator".to_string(),
                outcome: "skipped".to_string(),
                notes: skip_reason.trim().to_string(),
                created_at: now.clone(),
                ..GoalEvidence::default()
            });
        }
    }

    let goal = &mut goals[index];
    goal.status = status;
    goal.updated_at = now.clone();
    goal.completed_at = if status == GoalStatus::Complete {
        Some(now)
    } else {
        None
    };
    let snapshot = goal.clone();
    save_goals(data_dir, workspace_id, &goals)?;
    write_ledger(data_dir, workspace_id, &snapshot)?;
    Ok(snapshot)
}

fn normalize_evidence(items: &[GoalEvidence], now: &str) -> Vec<GoalEvidence> {
    items
        .iter()
        .map(|item| {
            let mut out = item.clone();
            out.kind = {
                let trimmed = out.kind.trim();
                if trimmed.is_empty() {
                    "note".to_string()
                } else {
                    trimmed.to_string()
                }
            };
            out.label = out.label.trim().to_string();
            out.command = out.command.trim().to_string();
            out.outcome = out.outcome.trim().to_string();
            out.path = out.path.trim().to_string();
            out.url = out.url.trim().to_string();
            out.notes = out.notes.trim().to_string();
            if out.created_at.trim().is_empty() {
                out.created_at = now.to_string();
            }
            out
        })
        .collect()
}

/// Lint an existing goal for slop/completion-readiness without mutating it.
pub fn lint_goal(data_dir: &Path, workspace_id: &str, goal_id: &str) -> Result<slop::SlopReport> {
    let goal = get_goal(data_dir, workspace_id, goal_id)?;
    let iterations = load_iterations(data_dir, workspace_id, goal_id)?;
    let mut findings = slop::lint_create(&goal.title, &goal.objective, &goal.acceptance_criteria)
        .findings
        .clone();
    for (idx, ev) in goal.evidence.iter().enumerate() {
        findings.extend(slop::lint_evidence(&format!("goal.evidence[{idx}]"), ev));
    }
    for iteration in &iterations {
        for (idx, ev) in iteration.evidence.iter().enumerate() {
            findings.extend(slop::lint_evidence(
                &format!("iteration[{}].evidence[{idx}]", iteration.iteration_id),
                ev,
            ));
        }
    }
    let iteration_evidence: Vec<&GoalEvidence> =
        iterations.iter().flat_map(|i| i.evidence.iter()).collect();
    findings.extend(
        slop::lint_completion(&goal, &iteration_evidence, "")
            .findings
            .into_iter()
            // Only report the completion gate when the goal is trying to be done.
            .filter(|f| goal.status != GoalStatus::Draft || f.code != "complete-without-evidence"),
    );
    let blocking = findings
        .iter()
        .filter(|f| f.severity == slop::SlopSeverity::Blocking)
        .count();
    let warnings = findings
        .iter()
        .filter(|f| f.severity == slop::SlopSeverity::Warning)
        .count();
    Ok(slop::SlopReport {
        ok: blocking == 0,
        blocking,
        warnings,
        findings,
    })
}

// ---------------------------------------------------------------------------
// Markdown projections (ledger + context packet)
// ---------------------------------------------------------------------------

fn evidence_suffix(evidence: &GoalEvidence) -> String {
    let mut parts = Vec::new();
    if !evidence.command.is_empty() {
        parts.push(format!("`{}`", evidence.command));
    }
    if !evidence.outcome.is_empty() {
        parts.push(format!("outcome: {}", evidence.outcome));
    }
    if !evidence.path.is_empty() {
        parts.push(format!("path: {}", evidence.path));
    }
    if !evidence.url.is_empty() {
        parts.push(format!("url: {}", evidence.url));
    }
    if !evidence.notes.is_empty() {
        parts.push(evidence.notes.clone());
    }
    if parts.is_empty() {
        String::new()
    } else {
        format!(" - {}", parts.join("; "))
    }
}

fn write_field(out: &mut String, label: &str, value: &str) {
    let value = value.trim();
    out.push_str(&format!("## {label}\n\n"));
    if value.is_empty() {
        out.push_str("Not recorded.\n\n");
    } else {
        out.push_str(&format!("{value}\n\n"));
    }
}

fn write_list(out: &mut String, label: &str, values: &[String]) {
    out.push_str(&format!("## {label}\n\n"));
    if values.is_empty() {
        out.push_str("- Not recorded.\n\n");
        return;
    }
    for value in values {
        out.push_str(&format!("- {value}\n"));
    }
    out.push('\n');
}

/// Regenerate and persist the human-readable ledger for a goal.
pub fn write_ledger(data_dir: &Path, workspace_id: &str, goal: &GoalRecord) -> Result<()> {
    let iterations = load_iterations(data_dir, workspace_id, &goal.goal_id)?;
    let mut out = String::new();
    out.push_str(&format!("# Goal Ledger: {}\n\n", goal.title));
    write_field(&mut out, "Live status", goal.status.as_str());
    write_field(&mut out, "Goal ID", &goal.goal_id);
    write_field(&mut out, "Objective", &goal.objective);
    write_field(&mut out, "Current tranche", &goal.current_tranche);
    write_field(&mut out, "Runtime preference", &goal.runtime_preference);
    write_field(&mut out, "Preferred model", &goal.preferred_model);
    write_list(&mut out, "Acceptance Criteria", &goal.acceptance_criteria);
    write_list(&mut out, "Allowed Surface", &goal.allowed_surface);
    write_list(&mut out, "Verification Commands", &goal.verification_commands);
    write_list(
        &mut out,
        "Verification Profile IDs",
        &goal.verification_profile_ids,
    );
    write_field(&mut out, "Resumption Notes", &goal.resumption_notes);
    out.push_str("## Completion Gate\n\n");
    if goal.status == GoalStatus::Complete {
        out.push_str("- Complete: verification evidence or explicit skip reason is recorded.\n\n");
    } else {
        out.push_str(
            "- Not complete until verification evidence or an explicit skip reason is recorded.\n\n",
        );
    }
    out.push_str("## Evidence\n\n");
    if goal.evidence.is_empty() {
        out.push_str("- No evidence recorded yet.\n");
    }
    for evidence in &goal.evidence {
        out.push_str(&format!(
            "- {}{}\n",
            fallback(&evidence.label, &evidence.kind),
            evidence_suffix(evidence)
        ));
    }
    out.push_str("\n## Iteration Log\n\n");
    if iterations.is_empty() {
        out.push_str("- No iterations recorded yet.\n");
    }
    for iteration in &iterations {
        out.push_str(&format!(
            "- {}: {}",
            fallback(&iteration.role, "worker"),
            iteration.summary
        ));
        if !iteration.outcome.is_empty() {
            out.push_str(&format!(" ({})", iteration.outcome));
        }
        out.push('\n');
        for path in &iteration.files_touched {
            out.push_str(&format!("  - touched: `{path}`\n"));
        }
        for evidence in &iteration.evidence {
            out.push_str(&format!(
                "  - evidence: {}{}\n",
                fallback(&evidence.label, &evidence.kind),
                evidence_suffix(evidence)
            ));
        }
    }
    let path = goal_ledger_path(data_dir, workspace_id, &goal.goal_id);
    write_bytes_atomic(&path, out.as_bytes())
}

/// Read the ledger, regenerating it on first access if missing.
pub fn read_ledger(data_dir: &Path, workspace_id: &str, goal_id: &str) -> Result<String> {
    let goal = get_goal(data_dir, workspace_id, goal_id)?;
    let path = goal_ledger_path(data_dir, workspace_id, goal_id);
    match fs::read_to_string(&path) {
        Ok(content) => Ok(content),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            write_ledger(data_dir, workspace_id, &goal)?;
            fs::read_to_string(&path).map_err(|e| io_err(&path, e))
        }
        Err(err) => Err(io_err(&path, err)),
    }
}

/// Build the bounded context packet a worker runtime is handed. It carries the
/// objective surface and recent evidence, never raw source or secrets.
pub fn build_context_packet(data_dir: &Path, workspace_id: &str, goal_id: &str) -> Result<String> {
    let goal = get_goal(data_dir, workspace_id, goal_id)?;
    let iterations = load_iterations(data_dir, workspace_id, goal_id)?;
    let mut out = String::new();
    out.push_str("# xMustard Goal Context Packet\n\n");
    out.push_str(
        "Worker output is proposal/evidence, not automatic acceptance. Keep changes inside the \
         allowed surface and return verification proof.\n\n",
    );
    write_field(&mut out, "Goal ID", &goal.goal_id);
    write_field(&mut out, "Status", goal.status.as_str());
    write_field(&mut out, "Title", &goal.title);
    write_field(&mut out, "Objective", &goal.objective);
    write_field(&mut out, "Current tranche", &goal.current_tranche);
    write_field(&mut out, "Runtime preference", &goal.runtime_preference);
    write_field(&mut out, "Preferred model", &goal.preferred_model);
    write_list(&mut out, "Acceptance Criteria", &goal.acceptance_criteria);
    write_list(&mut out, "Allowed Surface", &goal.allowed_surface);
    write_list(&mut out, "Verification Commands", &goal.verification_commands);
    write_list(
        &mut out,
        "Verification Profile IDs",
        &goal.verification_profile_ids,
    );
    write_field(&mut out, "Resumption Notes", &goal.resumption_notes);
    out.push_str("## Recent Iterations\n\n");
    if iterations.is_empty() {
        out.push_str("- No iterations recorded yet.\n");
    }
    let start = iterations.len().saturating_sub(5);
    for iteration in &iterations[start..] {
        out.push_str(&format!(
            "- {}: {}",
            fallback(&iteration.role, "worker"),
            iteration.summary
        ));
        if !iteration.outcome.is_empty() {
            out.push_str(&format!(" ({})", iteration.outcome));
        }
        out.push('\n');
        for evidence in &iteration.evidence {
            out.push_str(&format!(
                "  - evidence: {}{}\n",
                fallback(&evidence.label, &evidence.kind),
                evidence_suffix(evidence)
            ));
        }
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::slop::SlopSeverity;
    use super::*;
    use tempfile::TempDir;

    fn create_req(title: &str, objective: &str) -> GoalCreateRequest {
        GoalCreateRequest {
            title: title.to_string(),
            objective: objective.to_string(),
            verification_commands: vec!["cargo test".to_string()],
            runtime_preference: "opencode".to_string(),
            preferred_model: "opencode-go/deepseek-v4-pro".to_string(),
            ..GoalCreateRequest::default()
        }
    }

    #[test]
    fn status_round_trips_through_json() {
        for status in [
            GoalStatus::Draft,
            GoalStatus::Active,
            GoalStatus::Blocked,
            GoalStatus::Complete,
            GoalStatus::Archived,
        ] {
            let json = serde_json::to_string(&status).unwrap();
            let back: GoalStatus = serde_json::from_str(&json).unwrap();
            assert_eq!(status, back);
            assert_eq!(json, format!("\"{}\"", status.as_str()));
        }
        assert!(GoalStatus::parse("  COMPLETE ").is_ok());
        assert!(GoalStatus::parse("bogus").is_err());
    }

    #[test]
    fn slug_and_compact_match_go_rules() {
        assert_eq!(slug_id("Build Rust Goal CLI"), "build-rust-goal-cli");
        assert_eq!(slug_id("  Hello, World!! "), "hello-world");
        assert!(slug_id("日本語").starts_with("goal-"));
        assert_eq!(compact_timestamp("2026-06-16T01:11:45.123456789Z").len(), 15);
    }

    #[test]
    fn create_then_get_round_trips() {
        let dir = TempDir::new().unwrap();
        let (goal, report) =
            create_goal(dir.path(), "ws1", &create_req("Build CLI", "Ship a Rust goal CLI")).unwrap();
        assert_eq!(goal.goal_id, "build-cli");
        assert_eq!(goal.status, GoalStatus::Draft);
        assert!(report.ok);
        let fetched = get_goal(dir.path(), "ws1", "build-cli").unwrap();
        assert_eq!(fetched, goal);
        // goals.json is valid JSON the Go shell can read.
        let raw = fs::read_to_string(goals_path(dir.path(), "ws1")).unwrap();
        assert!(raw.contains("\"status\": \"draft\""));
    }

    #[test]
    fn create_rejects_refusal_objective() {
        let dir = TempDir::new().unwrap();
        let req = create_req("X", "As an AI, I cannot do this task");
        let err = create_goal(dir.path(), "ws1", &req).unwrap_err();
        assert!(matches!(err, GoalError::SlopBlocked(_)));
    }

    #[test]
    fn duplicate_titles_get_unique_ids() {
        let dir = TempDir::new().unwrap();
        let (a, _) = create_goal(dir.path(), "ws1", &create_req("Dup", "first objective here")).unwrap();
        let (b, _) = create_goal(dir.path(), "ws1", &create_req("Dup", "second objective here")).unwrap();
        assert_eq!(a.goal_id, "dup");
        assert_ne!(a.goal_id, b.goal_id);
        assert!(b.goal_id.starts_with("dup-"));
    }

    #[test]
    fn iteration_rejects_empty_summary_but_keeps_real_one() {
        let dir = TempDir::new().unwrap();
        create_goal(dir.path(), "ws1", &create_req("Goal", "do the thing well")).unwrap();
        let empty = GoalIterationAppendRequest {
            summary: "   ".to_string(),
            ..Default::default()
        };
        assert!(matches!(
            append_iteration(dir.path(), "ws1", "goal", &empty),
            Err(GoalError::SlopBlocked(_))
        ));
        let real = GoalIterationAppendRequest {
            role: "builder".to_string(),
            summary: "Implemented the create path and wired the CLI".to_string(),
            ..Default::default()
        };
        let (iter, report) = append_iteration(dir.path(), "ws1", "goal", &real).unwrap();
        assert_eq!(iter.iteration_id, format!("{}-001", &iter.iteration_id[..15]));
        assert!(report.ok);
    }

    #[test]
    fn completion_gate_blocks_without_evidence() {
        let dir = TempDir::new().unwrap();
        create_goal(dir.path(), "ws1", &create_req("Goal", "do the thing well")).unwrap();
        // No evidence, no skip reason -> blocked.
        let err = update_status(dir.path(), "ws1", "goal", GoalStatus::Complete, "").unwrap_err();
        assert!(matches!(err, GoalError::CompletionBlocked(_)));
        // Skip reason -> allowed, recorded as audit evidence.
        let goal = update_status(
            dir.path(),
            "ws1",
            "goal",
            GoalStatus::Complete,
            "demo run, verification skipped intentionally",
        )
        .unwrap();
        assert_eq!(goal.status, GoalStatus::Complete);
        assert!(goal.completed_at.is_some());
        assert!(goal.evidence.iter().any(|e| e.kind == "verification-skip"));
    }

    #[test]
    fn completion_gate_passes_with_real_evidence() {
        let dir = TempDir::new().unwrap();
        create_goal(dir.path(), "ws1", &create_req("Goal", "do the thing well")).unwrap();
        let req = GoalIterationAppendRequest {
            role: "verifier".to_string(),
            summary: "Ran the test suite to completion".to_string(),
            evidence: vec![GoalEvidence {
                kind: "test".to_string(),
                command: "cargo test".to_string(),
                outcome: "pass".to_string(),
                ..Default::default()
            }],
            ..Default::default()
        };
        append_iteration(dir.path(), "ws1", "goal", &req).unwrap();
        let goal = update_status(dir.path(), "ws1", "goal", GoalStatus::Complete, "").unwrap();
        assert_eq!(goal.status, GoalStatus::Complete);
        assert!(!goal.evidence.iter().any(|e| e.kind == "verification-skip"));
    }

    #[test]
    fn unverifiable_success_evidence_warns() {
        let ev = GoalEvidence {
            kind: "note".to_string(),
            outcome: "success".to_string(),
            ..Default::default()
        };
        let findings = slop::lint_evidence("evidence[0]", &ev);
        assert!(
            findings
                .iter()
                .any(|f| f.code == "unverifiable-success" && f.severity == SlopSeverity::Warning)
        );
    }

    #[test]
    fn ledger_and_context_render_without_panicking() {
        let dir = TempDir::new().unwrap();
        create_goal(dir.path(), "ws1", &create_req("Render", "render the ledger surface")).unwrap();
        let ledger = read_ledger(dir.path(), "ws1", "render").unwrap();
        assert!(ledger.contains("# Goal Ledger: Render"));
        assert!(ledger.contains("Completion Gate"));
        let packet = build_context_packet(dir.path(), "ws1", "render").unwrap();
        assert!(packet.contains("Worker output is proposal/evidence"));
        assert!(packet.contains("opencode-go/deepseek-v4-pro"));
    }

    #[test]
    fn get_missing_goal_is_not_found() {
        let dir = TempDir::new().unwrap();
        assert!(matches!(
            get_goal(dir.path(), "ws1", "nope"),
            Err(GoalError::NotFound(_))
        ));
    }

    #[test]
    fn slop_linter_table() {
        // Each row calls a public lint function and asserts the expected
        // finding codes with their mandated severity.

        enum Op {
            Create { objective: &'static str },
            Iter { summary: &'static str, objective: &'static str },
            Evidence { outcome: &'static str },
        }

        struct Row {
            name: &'static str,
            op: Op,
            want: Vec<(&'static str, SlopSeverity)>,
        }

        let rows = [
            // (1) Refusal markers -> Blocking "refusal-marker"
            Row {
                name: "refusal-as-an-ai",
                op: Op::Create { objective: "As an AI, I cannot do this" },
                want: vec![("refusal-marker", SlopSeverity::Blocking)],
            },
            Row {
                name: "refusal-i-cannot",
                op: Op::Create { objective: "I cannot help with this task" },
                want: vec![("refusal-marker", SlopSeverity::Blocking)],
            },
            // (2) Placeholder markers -> Warning "placeholder-marker"
            Row {
                name: "placeholder-todo",
                op: Op::Create { objective: "TODO: implement something" },
                want: vec![("placeholder-marker", SlopSeverity::Warning)],
            },
            Row {
                name: "placeholder-tbd",
                op: Op::Create { objective: "tbd later discussion" },
                want: vec![("placeholder-marker", SlopSeverity::Warning)],
            },
            Row {
                name: "placeholder-lorem-ipsum",
                op: Op::Create { objective: "Lorem ipsum dolor sit" },
                want: vec![("placeholder-marker", SlopSeverity::Warning)],
            },
            // (3) Evidence pass/ok with no command/path/url -> Warning
            Row {
                name: "unverifiable-pass",
                op: Op::Evidence { outcome: "pass" },
                want: vec![("unverifiable-success", SlopSeverity::Warning)],
            },
            Row {
                name: "unverifiable-ok",
                op: Op::Evidence { outcome: "ok" },
                want: vec![("unverifiable-success", SlopSeverity::Warning)],
            },
            // (4) Summary equals objective -> Warning
            Row {
                name: "summary-echoes-objective",
                op: Op::Iter { summary: "build the goal cli", objective: "build the goal cli" },
                want: vec![("summary-echoes-objective", SlopSeverity::Warning)],
            },
            // (5) Empty/whitespace required summary -> Blocking
            Row {
                name: "empty-required-empty",
                op: Op::Iter { summary: "", objective: "do the thing" },
                want: vec![("empty-required", SlopSeverity::Blocking)],
            },
            Row {
                name: "empty-required-whitespace",
                op: Op::Iter { summary: "   ", objective: "do the thing" },
                want: vec![("empty-required", SlopSeverity::Blocking)],
            },
        ];

        for row in &rows {
            let findings: Vec<slop::SlopFinding> = match &row.op {
                Op::Create { objective } => {
                    slop::lint_create("Test", objective, &[]).findings
                }
                Op::Iter { summary, objective } => {
                    let req = GoalIterationAppendRequest {
                        summary: summary.to_string(),
                        ..Default::default()
                    };
                    slop::lint_iteration(&req, objective).findings
                }
                Op::Evidence { outcome } => {
                    let ev = GoalEvidence {
                        kind: "test".to_string(),
                        outcome: outcome.to_string(),
                        ..Default::default()
                    };
                    slop::lint_evidence("evidence[0]", &ev)
                }
            };
            for (code, severity) in &row.want {
                assert!(
                    findings.iter().any(|f| f.code == *code && f.severity == *severity),
                    "[{}] expected finding ({code}, {severity:?}) not in {findings:?}",
                    row.name,
                );
            }
        }
    }
}
