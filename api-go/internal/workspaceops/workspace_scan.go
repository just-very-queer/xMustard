package workspaceops

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"xmustard/api-go/internal/rustcore"
)

var (
	bugHeaderRE = regexp.MustCompile(`^###\s+(P\d_\d{2}M\d{2}_\d{3})\.\s+(.*)$`)
	statusRE    = regexp.MustCompile(`^- Status \(([^)]+)\):\s*(.*)$`)
	evidenceRE  = regexp.MustCompile("`([^`:]+)(?::(\\d+))?`")
)

func ScanWorkspace(dataDir string, workspaceID string) (*workspaceSnapshot, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	root := workspace.RootPath
	ledgerPath := latestBugLedger(root)
	verdictPaths := verdictBundles(root)

	trackerIssues, err := loadTrackerIssues(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	fixes, err := loadFixRecords(dataDir, workspaceID, "")
	if err != nil {
		return nil, err
	}
	runReviews, err := loadRunReviews(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	runbooks, err := listRunbooksForScan(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	verificationProfiles, err := listVerificationProfilesForScan(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	ticketContexts, err := loadTicketContexts(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	threatModels, err := loadThreatModels(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	runs, err := listRuns(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}

	issues := []issueRecord{}
	if ledgerPath != "" {
		issues, err = parseLedger(ledgerPath)
		if err != nil {
			return nil, err
		}
	}
	if len(verdictPaths) > 0 && len(issues) > 0 {
		issues, err = applyVerdicts(issues, verdictPaths, root)
		if err != nil {
			return nil, err
		}
	}
	issues = mergeTrackerIssues(issues, trackerIssues)
	issues, err = applyIssueOverrides(dataDir, workspaceID, issues)
	if err != nil {
		return nil, err
	}
	issues = annotateReviewReady(issues, runs, fixes, runReviews)
	issues = normalizeIssueEvidence(root, issues)
	issues = applyIssueDrift(root, issues)

	rustSignals, err := rustcore.ScanSignals(context.Background(), root)
	if err != nil {
		return nil, err
	}
	signals := convertDiscoverySignals(rustSignals)
	sources, err := buildSourceRecords(ledgerPath, issues, verdictPaths, signals)
	if err != nil {
		return nil, err
	}
	repoMap, err := rustcore.BuildRepoMap(context.Background(), workspaceID, root)
	if err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "repo_map.json"), repoMap); err != nil {
		return nil, err
	}
	sources = append(sources, buildTrackerSources(dataDir, workspaceID, trackerIssues, fixes, runbooks, runReviews, verificationProfiles, ticketContexts, threatModels)...)
	driftSummary := buildDriftSummary(issues)
	runtimes, err := DetectRuntimes(dataDir)
	if err != nil {
		return nil, err
	}
	treeSummary := summarizeTree(root)
	now := nowUTC()
	workspace.LatestScanAt = ptr(now)
	workspace.UpdatedAt = ptr(now)
	snapshot := &workspaceSnapshot{
		ScannerVersion: scannerVersion,
		Workspace:      workspace,
		Summary: map[string]int{
			"issues_total":          len(issues),
			"issues_fixed":          countIssuesFixed(issues),
			"issues_open":           countIssuesOpen(issues),
			"review_ready_total":    countReviewReady(issues),
			"review_queue_total":    countReviewQueue(issues),
			"signals_total":         len(signals),
			"signals_promoted":      countSignalsPromoted(signals),
			"drift_total":           countDriftFlags(issues),
			"sources_total":         len(sources),
			"tracker_issues_total":  len(trackerIssues),
			"fixes_total":           len(fixes),
			"runbooks_total":        len(runbooks),
			"ticket_contexts_total": len(ticketContexts),
			"threat_models_total":   len(threatModels),
			"repo_map_files":        repoMap.TotalFiles,
			"tree_files":            treeSummary["files"],
			"tree_directories":      treeSummary["directories"],
		},
		Issues:         issues,
		Signals:        signals,
		Sources:        sources,
		DriftSummary:   driftSummary,
		Runtimes:       toSnapshotRuntimes(runtimes),
		LatestLedger:   optionalString(ledgerPath),
		LatestVerdicts: optionalString(lastString(verdictPaths)),
		GeneratedAt:    now,
	}
	if err := saveWorkspaceRecord(dataDir, workspace); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json"), snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func latestBugLedger(root string) string {
	bugDir := filepath.Join(root, "docs", "bugs")
	entries, err := os.ReadDir(bugDir)
	if err != nil {
		return ""
	}
	candidates := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "Bugs_") || !strings.HasSuffix(name, ".md") {
			continue
		}
		if strings.Contains(name, "_status_") {
			continue
		}
		candidates = append(candidates, filepath.Join(bugDir, name))
	}
	slices.Sort(candidates)
	return lastString(candidates)
}

func verdictBundles(root string) []string {
	searchRoots := []string{root, filepath.Dir(root)}
	candidates := map[string]struct{}{}
	for _, searchRoot := range searchRoots {
		_ = filepath.WalkDir(searchRoot, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry == nil || entry.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, "_verdicts.json") {
				candidates[path] = struct{}{}
			}
			return nil
		})
	}
	items := make([]string, 0, len(candidates))
	for path := range candidates {
		items = append(items, path)
	}
	slices.Sort(items)
	return items
}

func parseLedger(ledgerPath string) ([]issueRecord, error) {
	content, err := os.ReadFile(ledgerPath)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(content), "\n")
	issues := []issueRecord{}
	currentID := ""
	currentTitle := ""
	block := []string{}
	flush := func() {
		if currentID == "" {
			return
		}
		issues = append(issues, buildIssueFromBlock(ledgerPath, currentID, currentTitle, block))
		currentID = ""
		currentTitle = ""
		block = []string{}
	}
	for _, line := range lines {
		match := bugHeaderRE.FindStringSubmatch(line)
		if len(match) == 3 {
			flush()
			currentID = match[1]
			currentTitle = match[2]
			continue
		}
		if currentID != "" {
			block = append(block, line)
		}
	}
	flush()
	return issues, nil
}

func buildIssueFromBlock(ledgerPath string, bugID string, title string, lines []string) issueRecord {
	var summary *string
	var impact *string
	notes := []string{}
	evidence := []evidenceRef{}
	verificationEvidence := []evidenceRef{}
	testsAdded := []string{}
	testsPassed := []string{}
	docStatus := "open"
	var verifiedAt *string
	inEvidence := false
	inVerification := false

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		switch {
		case strings.HasPrefix(line, "- Summary:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "- Summary:"))
			summary = optionalString(value)
		case strings.HasPrefix(line, "- Impact:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "- Impact:"))
			impact = optionalString(value)
		case strings.HasPrefix(line, "- Evidence:"):
			inEvidence = true
			inVerification = false
		case strings.HasPrefix(line, "- Verification/update evidence:"):
			inEvidence = false
			inVerification = true
		case strings.HasPrefix(line, "- Remaining gap:") || strings.HasPrefix(line, "- Correction"):
			notes = append(notes, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			inEvidence = false
			inVerification = false
		default:
			if match := statusRE.FindStringSubmatch(rawLine); len(match) == 3 {
				verifiedAt = optionalString(strings.TrimSpace(match[1]))
				docStatus = normalizeIssueStatus(strings.TrimSpace(match[2]))
				continue
			}
			if inEvidence && strings.HasPrefix(line, "- ") {
				evidence = append(evidence, extractEvidence(line)...)
				if strings.Contains(line, "pytest") || strings.Contains(line, "npm run") {
					testsPassed = append(testsPassed, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
				}
			} else if inVerification && strings.HasPrefix(line, "- ") {
				verificationEvidence = append(verificationEvidence, extractEvidence(line)...)
				if strings.Contains(line, "pytest") || strings.Contains(line, "npm run") {
					testsPassed = append(testsPassed, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
				}
			} else if strings.HasPrefix(line, "- ") && strings.Contains(line, "test/") {
				testsAdded = append(testsAdded, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			}
		}
	}

	issueStatus := "open"
	if docStatus == "fixed" || docStatus == "resolved" {
		issueStatus = "resolved"
	} else if docStatus == "partial" {
		issueStatus = "partial"
	}
	fingerprint := sha1.Sum([]byte(ledgerPath + ":" + bugID + ":" + title))
	fingerprintHex := hex.EncodeToString(fingerprint[:])
	return issueRecord{
		BugID:                bugID,
		Title:                title,
		Severity:             strings.SplitN(bugID, "_", 2)[0],
		IssueStatus:          issueStatus,
		Source:               "ledger",
		SourceDoc:            ptr(ledgerPath),
		DocStatus:            docStatus,
		CodeStatus:           "open",
		Summary:              summary,
		Impact:               impact,
		Evidence:             evidence,
		VerificationEvidence: verificationEvidence,
		TestsAdded:           dedupeSortedStrings(testsAdded),
		TestsPassed:          dedupeSortedStrings(testsPassed),
		DriftFlags:           []string{},
		Labels:               []string{},
		Notes:                optionalString(strings.Join(notes, " | ")),
		VerifiedAt:           verifiedAt,
		VerifiedBy:           ptr("ledger"),
		NeedsFollowup:        docStatus != "fixed",
		ReviewReadyRuns:      []string{},
		Fingerprint:          &fingerprintHex,
		UpdatedAt:            nowUTC(),
	}
}

func normalizeIssueStatus(value string) string {
	lowered := strings.ToLower(value)
	switch {
	case strings.Contains(lowered, "already fixed"), strings.HasPrefix(lowered, "fixed"):
		return "fixed"
	case strings.Contains(lowered, "partial"):
		return "partial"
	case strings.Contains(lowered, "resolved"):
		return "resolved"
	default:
		return "open"
	}
}

func extractEvidence(value string) []evidenceRef {
	matches := evidenceRE.FindAllStringSubmatch(value, -1)
	items := []evidenceRef{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		path := strings.TrimSpace(match[1])
		lowered := strings.ToLower(path)
		if strings.Contains(lowered, " ") || strings.HasPrefix(lowered, "python") || strings.HasPrefix(lowered, "pytest") || strings.HasPrefix(lowered, "npm ") {
			continue
		}
		if !strings.Contains(path, "/") && !strings.Contains(filepath.Base(path), ".") {
			continue
		}
		ref := evidenceRef{Path: path}
		if len(match) > 2 && match[2] != "" {
			line := atoiSafe(match[2])
			if line > 0 {
				ref.Line = ptr(line)
			}
		}
		items = append(items, ref)
	}
	return items
}

func applyVerdicts(issues []issueRecord, verdictPaths []string, repoRoot string) ([]issueRecord, error) {
	verdictByID := map[string]map[string]any{}
	for _, verdictPath := range verdictPaths {
		items, err := loadVerdictItems(verdictPath)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if bugID, ok := item["id"].(string); ok {
				verdictByID[bugID] = item
			}
		}
	}
	updated := make([]issueRecord, 0, len(issues))
	for _, issue := range issues {
		verdict, ok := verdictByID[issue.BugID]
		if !ok {
			updated = append(updated, issue)
			continue
		}
		codeStatus := normalizeIssueStatus(toText(verdict["verdict"]))
		verificationEvidence := append([]evidenceRef{}, issue.VerificationEvidence...)
		if entries, ok := verdict["evidence"].([]any); ok {
			for _, raw := range entries {
				text, ok := raw.(string)
				if !ok {
					continue
				}
				path, lineText, _ := strings.Cut(text, ":")
				normalized := normalizeEvidencePath(repoRoot, path)
				ref := evidenceRef{
					Path:           path,
					NormalizedPath: optionalString(normalized),
				}
				if lineText != "" {
					line := atoiSafe(lineText)
					if line > 0 {
						ref.Line = ptr(line)
					}
				}
				if normalized != "" {
					ref.PathExists = ptr(fileExists(filepath.Join(repoRoot, normalized)))
					ref.PathScope = ptr("repo-relative")
				} else {
					ref.PathExists = ptr(false)
					ref.PathScope = ptr("unresolved")
				}
				verificationEvidence = append(verificationEvidence, ref)
			}
		}
		driftFlags := append([]string{}, issue.DriftFlags...)
		if issue.DocStatus == "fixed" && codeStatus != "fixed" {
			driftFlags = append(driftFlags, "doc_fixed_code_not_fixed")
		}
		if issue.DocStatus != "fixed" && codeStatus == "fixed" {
			driftFlags = append(driftFlags, "code_fixed_doc_not_fixed")
		}
		issue.CodeStatus = codeStatus
		issue.VerificationEvidence = verificationEvidence
		issue.VerifiedBy = ptr("codex")
		if concise := strings.TrimSpace(toText(verdict["concise_restatement"])); concise != "" {
			issue.Notes = ptr(concise)
		}
		issue.DriftFlags = dedupeSortedStrings(driftFlags)
		issue.NeedsFollowup = codeStatus != "fixed" || issue.DocStatus != "fixed"
		updated = append(updated, issue)
	}
	return updated, nil
}

func buildSourceRecords(ledgerPath string, issues []issueRecord, verdictPaths []string, signals []discoverySignal) ([]sourceRecord, error) {
	sources := []sourceRecord{}
	if ledgerPath != "" {
		if info, err := os.Stat(ledgerPath); err == nil && !info.IsDir() {
			sources = append(sources, sourceRecord{
				SourceID:    "src_" + shortPathHash(ledgerPath),
				Kind:        "ledger",
				Label:       filepath.Base(ledgerPath),
				Path:        ledgerPath,
				RecordCount: len(issues),
				ModifiedAt:  optionalString(fileTimestamp(info)),
				Notes:       optionalString("Canonical markdown ledger source."),
			})
		}
	}
	for _, verdictPath := range verdictPaths {
		items, err := loadVerdictItems(verdictPath)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(verdictPath)
		if err != nil {
			continue
		}
		sources = append(sources, sourceRecord{
			SourceID:    "src_" + shortPathHash(verdictPath),
			Kind:        "verdict_bundle",
			Label:       filepath.Base(verdictPath),
			Path:        verdictPath,
			RecordCount: len(items),
			ModifiedAt:  optionalString(fileTimestamp(info)),
			Notes:       optionalString("Recursive verdict bundle ingestion."),
		})
	}
	scannerCounts := map[string]int{}
	for _, signal := range signals {
		scannerCounts[signal.Kind]++
	}
	keys := make([]string, 0, len(scannerCounts))
	for key := range scannerCounts {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, kind := range keys {
		sources = append(sources, sourceRecord{
			SourceID:    "src_scanner_" + kind,
			Kind:        "scanner",
			Label:       kind,
			Path:        kind,
			RecordCount: scannerCounts[kind],
			Notes:       optionalString("Auto-discovery heuristic feed."),
		})
	}
	return sources, nil
}

func buildTrackerSources(dataDir string, workspaceID string, trackerIssues []issueRecord, fixes []FixRecord, runbooks []RunbookRecord, runReviews []RunReviewRecord, verificationProfiles []verificationProfileRecord, ticketContexts []TicketContextRecord, threatModels []ThreatModelRecord) []sourceRecord {
	type trackerSource struct {
		path        string
		kind        string
		notes       string
		recordCount int
	}
	items := []trackerSource{
		{path: filepath.Join(dataDir, "workspaces", workspaceID, "tracker_issues.json"), kind: "tracker_issue", notes: "Tracker-native issue records.", recordCount: len(trackerIssues)},
		{path: filepath.Join(dataDir, "workspaces", workspaceID, "fix_records.json"), kind: "fix_record", notes: "Agent and operator fix history.", recordCount: len(fixes)},
		{path: filepath.Join(dataDir, "workspaces", workspaceID, "runbooks.json"), kind: "fix_record", notes: "Tracker-native reusable runbooks.", recordCount: len(runbooks)},
		{path: filepath.Join(dataDir, "workspaces", workspaceID, "run_reviews.json"), kind: "fix_record", notes: "Run review dispositions and review audit history.", recordCount: len(runReviews)},
		{path: filepath.Join(dataDir, "workspaces", workspaceID, "verification_profiles.json"), kind: "fix_record", notes: "Workspace verification profiles and saved test commands.", recordCount: len(verificationProfiles)},
		{path: filepath.Join(dataDir, "workspaces", workspaceID, "ticket_contexts.json"), kind: "ticket_context", notes: "Imported and curated upstream ticket context with acceptance criteria.", recordCount: len(ticketContexts)},
		{path: filepath.Join(dataDir, "workspaces", workspaceID, "threat_models.json"), kind: "threat_model", notes: "Issue-level threat models with assets, trust boundaries, abuse paths, and mitigations.", recordCount: len(threatModels)},
		{path: filepath.Join(dataDir, "workspaces", workspaceID, "repo_map.json"), kind: "repo_map", notes: "Workspace structural repo map and notable files.", recordCount: 1},
	}
	sources := []sourceRecord{}
	for _, item := range items {
		info, err := os.Stat(item.path)
		if err != nil || info.IsDir() {
			continue
		}
		now := nowUTC()
		sources = append(sources, sourceRecord{
			SourceID:    "src_" + shortPathHash(item.path),
			Kind:        item.kind,
			Label:       filepath.Base(item.path),
			Path:        item.path,
			RecordCount: item.recordCount,
			ModifiedAt:  &now,
			Notes:       ptr(item.notes),
		})
	}
	return sources
}

func mergeTrackerIssues(imported []issueRecord, tracked []issueRecord) []issueRecord {
	merged := map[string]issueRecord{}
	for _, item := range imported {
		merged[item.BugID] = item
	}
	for _, trackedIssue := range tracked {
		existing, ok := merged[trackedIssue.BugID]
		if !ok {
			merged[trackedIssue.BugID] = trackedIssue
			continue
		}
		existing.Title = firstNonEmpty(trackedIssue.Title, existing.Title)
		existing.Severity = firstNonEmpty(trackedIssue.Severity, existing.Severity)
		existing.IssueStatus = trackedIssue.IssueStatus
		existing.Source = trackedIssue.Source
		if trackedIssue.SourceDoc != nil && strings.TrimSpace(*trackedIssue.SourceDoc) != "" {
			existing.SourceDoc = trackedIssue.SourceDoc
		}
		existing.DocStatus = firstNonEmpty(trackedIssue.DocStatus, existing.DocStatus)
		existing.CodeStatus = firstNonEmpty(trackedIssue.CodeStatus, existing.CodeStatus)
		if trackedIssue.Summary != nil && strings.TrimSpace(*trackedIssue.Summary) != "" {
			existing.Summary = trackedIssue.Summary
		}
		if trackedIssue.Impact != nil && strings.TrimSpace(*trackedIssue.Impact) != "" {
			existing.Impact = trackedIssue.Impact
		}
		if len(trackedIssue.Evidence) > 0 {
			existing.Evidence = trackedIssue.Evidence
		}
		if len(trackedIssue.VerificationEvidence) > 0 {
			existing.VerificationEvidence = trackedIssue.VerificationEvidence
		}
		if len(trackedIssue.TestsAdded) > 0 {
			existing.TestsAdded = trackedIssue.TestsAdded
		}
		if len(trackedIssue.TestsPassed) > 0 {
			existing.TestsPassed = trackedIssue.TestsPassed
		}
		existing.Labels = dedupeSortedStrings(append(existing.Labels, trackedIssue.Labels...))
		if trackedIssue.Notes != nil && strings.TrimSpace(*trackedIssue.Notes) != "" {
			existing.Notes = trackedIssue.Notes
		}
		if trackedIssue.VerifiedAt != nil {
			existing.VerifiedAt = trackedIssue.VerifiedAt
		}
		if trackedIssue.VerifiedBy != nil {
			existing.VerifiedBy = trackedIssue.VerifiedBy
		}
		existing.NeedsFollowup = trackedIssue.NeedsFollowup
		if trackedIssue.Fingerprint != nil && strings.TrimSpace(*trackedIssue.Fingerprint) != "" {
			existing.Fingerprint = trackedIssue.Fingerprint
		}
		existing.UpdatedAt = trackedIssue.UpdatedAt
		merged[trackedIssue.BugID] = existing
	}
	items := make([]issueRecord, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b issueRecord) int {
		if a.Severity == b.Severity {
			if a.BugID < b.BugID {
				return -1
			}
			if a.BugID > b.BugID {
				return 1
			}
			return 0
		}
		if a.Severity < b.Severity {
			return -1
		}
		return 1
	})
	return items
}

func applyIssueOverrides(dataDir string, workspaceID string, issues []issueRecord) ([]issueRecord, error) {
	path := filepath.Join(dataDir, "workspaces", workspaceID, "issue_overrides.json")
	overrides := map[string]map[string]any{}
	if err := readJSON(path, &overrides); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	updated := make([]issueRecord, 0, len(issues))
	for _, issue := range issues {
		override, ok := overrides[issue.BugID]
		if !ok {
			updated = append(updated, issue)
			continue
		}
		if value := strings.TrimSpace(toText(override["severity"])); value != "" {
			issue.Severity = value
		}
		if value := strings.TrimSpace(toText(override["issue_status"])); value != "" {
			issue.IssueStatus = value
		}
		if value := strings.TrimSpace(toText(override["doc_status"])); value != "" {
			issue.DocStatus = value
		}
		if value := strings.TrimSpace(toText(override["code_status"])); value != "" {
			issue.CodeStatus = value
		}
		if raw, ok := override["labels"].([]any); ok {
			labels := []string{}
			for _, item := range raw {
				if text, ok := item.(string); ok {
					labels = append(labels, text)
				}
			}
			issue.Labels = dedupeSortedStrings(labels)
		}
		if value := strings.TrimSpace(toText(override["notes"])); value != "" {
			issue.Notes = ptr(value)
		}
		if value, ok := override["needs_followup"].(bool); ok {
			issue.NeedsFollowup = value
		}
		if value := strings.TrimSpace(toText(override["updated_at"])); value != "" {
			issue.UpdatedAt = value
		}
		updated = append(updated, issue)
	}
	return updated, nil
}

func annotateReviewReady(issues []issueRecord, runs []runRecord, fixes []FixRecord, runReviews []RunReviewRecord) []issueRecord {
	fixedRunIDs := map[string]struct{}{}
	for _, item := range fixes {
		if item.RunID != nil && strings.TrimSpace(*item.RunID) != "" {
			fixedRunIDs[*item.RunID] = struct{}{}
		}
	}
	reviewedRunIDs := map[string]struct{}{}
	for _, item := range runReviews {
		reviewedRunIDs[item.RunID] = struct{}{}
	}
	pendingByIssue := map[string][]string{}
	for _, run := range runs {
		if run.IssueID == "workspace-query" || run.Status != "completed" {
			continue
		}
		if _, ok := fixedRunIDs[run.RunID]; ok {
			continue
		}
		if _, ok := reviewedRunIDs[run.RunID]; ok {
			continue
		}
		pendingByIssue[run.IssueID] = append(pendingByIssue[run.IssueID], run.RunID)
	}
	updated := make([]issueRecord, 0, len(issues))
	for _, issue := range issues {
		pending := pendingByIssue[issue.BugID]
		issue.ReviewReadyCount = len(pending)
		if len(pending) > 8 {
			pending = pending[:8]
		}
		issue.ReviewReadyRuns = pending
		updated = append(updated, issue)
	}
	return updated
}

func normalizeIssueEvidence(root string, issues []issueRecord) []issueRecord {
	updated := make([]issueRecord, 0, len(issues))
	for _, issue := range issues {
		issue.Evidence = normalizeEvidenceItems(root, issue.Evidence)
		issue.VerificationEvidence = normalizeEvidenceItems(root, issue.VerificationEvidence)
		updated = append(updated, issue)
	}
	return updated
}

func normalizeEvidenceItems(root string, items []evidenceRef) []evidenceRef {
	normalized := make([]evidenceRef, 0, len(items))
	for _, item := range items {
		normalizedPath := strings.TrimLeft(item.Path, "./")
		if item.NormalizedPath != nil && strings.TrimSpace(*item.NormalizedPath) != "" {
			normalizedPath = strings.TrimSpace(*item.NormalizedPath)
		}
		item.NormalizedPath = ptr(normalizedPath)
		item.PathExists = ptr(fileExists(filepath.Join(root, normalizedPath)))
		item.PathScope = ptr("repo-relative")
		normalized = append(normalized, item)
	}
	return normalized
}

func applyIssueDrift(root string, issues []issueRecord) []issueRecord {
	updated := make([]issueRecord, 0, len(issues))
	for _, issue := range issues {
		driftFlags := append([]string{}, issue.DriftFlags...)
		if issue.DocStatus == "partial" {
			driftFlags = append(driftFlags, "partial_fix")
		}
		if len(issue.TestsPassed) == 0 {
			driftFlags = append(driftFlags, "missing_verification_tests")
		}
		for _, evidence := range append(append([]evidenceRef{}, issue.Evidence...), issue.VerificationEvidence...) {
			if strings.TrimSpace(evidence.Path) == "" {
				continue
			}
			checkPath := evidence.Path
			if evidence.NormalizedPath != nil && strings.TrimSpace(*evidence.NormalizedPath) != "" {
				checkPath = *evidence.NormalizedPath
			}
			if evidence.PathExists != nil && !*evidence.PathExists || !fileExists(filepath.Join(root, checkPath)) {
				driftFlags = append(driftFlags, "missing_evidence:"+evidence.Path)
			}
		}
		issue.DriftFlags = dedupeSortedStrings(driftFlags)
		updated = append(updated, issue)
	}
	return updated
}

func buildDriftSummary(issues []issueRecord) map[string]int {
	summary := map[string]int{}
	for _, issue := range issues {
		for _, flag := range issue.DriftFlags {
			bucket := flag
			if before, _, ok := strings.Cut(flag, ":"); ok {
				bucket = before
			}
			summary[bucket]++
		}
	}
	keys := make([]string, 0, len(summary))
	for key := range summary {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if summary[a] == summary[b] {
			if a < b {
				return -1
			}
			if a > b {
				return 1
			}
			return 0
		}
		if summary[a] > summary[b] {
			return -1
		}
		return 1
	})
	ordered := map[string]int{}
	for _, key := range keys {
		ordered[key] = summary[key]
	}
	return ordered
}

func summarizeTree(root string) map[string]int {
	files := 0
	directories := 0
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == root {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if shouldIgnoreRelativePath(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			directories++
		} else {
			files++
		}
		return nil
	})
	return map[string]int{"files": files, "directories": directories}
}

func convertDiscoverySignals(items []rustcore.DiscoverySignal) []discoverySignal {
	signals := make([]discoverySignal, 0, len(items))
	for _, item := range items {
		evidence := make([]evidenceRef, 0, len(item.Evidence))
		for _, ref := range item.Evidence {
			evidenceItem := evidenceRef{Path: ref.Path}
			if ref.Line > 0 {
				evidenceItem.Line = ptr(ref.Line)
			}
			if strings.TrimSpace(ref.Excerpt) != "" {
				evidenceItem.Excerpt = ptr(ref.Excerpt)
			}
			evidence = append(evidence, evidenceItem)
		}
		fingerprint := strings.TrimSpace(item.Fingerprint)
		signals = append(signals, discoverySignal{
			SignalID:    item.SignalID,
			Kind:        item.Kind,
			Severity:    item.Severity,
			Title:       item.Title,
			Summary:     item.Summary,
			FilePath:    item.FilePath,
			Line:        item.Line,
			Evidence:    evidence,
			Tags:        item.Tags,
			Fingerprint: optionalString(fingerprint),
		})
	}
	return signals
}

func toSnapshotRuntimes(items []RuntimeCapabilities) []runtimeCapabilities {
	out := make([]runtimeCapabilities, 0, len(items))
	for _, item := range items {
		models := make([]runtimeModel, 0, len(item.Models))
		for _, model := range item.Models {
			models = append(models, runtimeModel{Runtime: model.Runtime, ID: model.ID})
		}
		out = append(out, runtimeCapabilities{
			Runtime:    item.Runtime,
			Available:  item.Available,
			BinaryPath: item.BinaryPath,
			Models:     models,
			Notes:      item.Notes,
		})
	}
	return out
}

func listRunbooksForScan(dataDir string, workspaceID string) ([]RunbookRecord, error) {
	snapshotPath := filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")
	if !fileExists(snapshotPath) {
		now := nowUTC()
		return []RunbookRecord{
			{RunbookID: "verify", WorkspaceID: workspaceID, Name: "Verify", Description: "Validate reproduction and current failure shape before changing code.", Scope: "issue", Template: "1. Reproduce or validate the reported behavior in the current tree.\n2. Confirm the cited evidence and likely failing surface.\n3. Stop after verification and return findings, impacted files, and candidate tests.", BuiltIn: true, CreatedAt: now, UpdatedAt: now},
			{RunbookID: "fix", WorkspaceID: workspaceID, Name: "Fix", Description: "Default bug-fix workflow with tests and tracker provenance.", Scope: "issue", Template: "1. Verify the bug still reproduces in the current workspace tree.\n2. Inspect the cited evidence paths before changing code.\n3. Make the minimal safe fix.\n4. Add or update tests that fail before the fix and pass after the fix.\n5. Record a fix entry with files changed, tests run, and agent provenance.", BuiltIn: true, CreatedAt: now, UpdatedAt: now},
			{RunbookID: "reproduce", WorkspaceID: workspaceID, Name: "Reproduce", Description: "Focus only on reproduction, scope, and proof.", Scope: "issue", Template: "1. Reproduce the reported bug.\n2. Narrow the failing boundary to concrete files and functions.\n3. Suggest the smallest next-step fix plan.\n4. Do not modify code.", BuiltIn: true, CreatedAt: now, UpdatedAt: now},
			{RunbookID: "drift-audit", WorkspaceID: workspaceID, Name: "Drift Audit", Description: "Compare tracker evidence, code state, and tests for drift.", Scope: "issue", Template: "1. Check whether the tracker claim still matches code.\n2. Identify missing evidence, missing tests, or outdated status.\n3. Return only drift findings and recommended tracker updates.", BuiltIn: true, CreatedAt: now, UpdatedAt: now},
		}, nil
	}
	return listRunbooks(dataDir, workspaceID)
}

func listVerificationProfilesForScan(dataDir string, workspaceID string) ([]verificationProfileRecord, error) {
	snapshotPath := filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json")
	if !fileExists(snapshotPath) {
		saved, err := loadSavedVerificationProfiles(dataDir, workspaceID)
		if err != nil {
			return nil, err
		}
		merged := map[string]verificationProfileRecord{
			"manual-check": defaultVerificationProfile(workspaceID),
		}
		for _, profile := range saved {
			merged[profile.ProfileID] = profile
		}
		items := make([]verificationProfileRecord, 0, len(merged))
		for _, profile := range merged {
			items = append(items, profile)
		}
		slices.SortFunc(items, func(a, b verificationProfileRecord) int {
			if a.BuiltIn != b.BuiltIn {
				if a.BuiltIn {
					return -1
				}
				return 1
			}
			left := strings.ToLower(a.Name)
			right := strings.ToLower(b.Name)
			if left == right {
				if a.CreatedAt < b.CreatedAt {
					return -1
				}
				if a.CreatedAt > b.CreatedAt {
					return 1
				}
				return 0
			}
			if left < right {
				return -1
			}
			return 1
		})
		return items, nil
	}
	return ListVerificationProfiles(dataDir, workspaceID)
}

func loadVerdictItems(verdictPath string) ([]map[string]any, error) {
	content, err := os.ReadFile(verdictPath)
	if err != nil {
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(content, &payload); err != nil {
		return nil, err
	}
	switch value := payload.(type) {
	case []any:
		items := []map[string]any{}
		for _, item := range value {
			if record, ok := item.(map[string]any); ok {
				items = append(items, record)
			}
		}
		return items, nil
	case map[string]any:
		if wrapped, ok := value["verdicts"].([]any); ok {
			items := []map[string]any{}
			for _, item := range wrapped {
				if record, ok := item.(map[string]any); ok {
					items = append(items, record)
				}
			}
			return items, nil
		}
	}
	return []map[string]any{}, nil
}

func normalizeEvidencePath(repoRoot string, rawPath string) string {
	path := filepath.Clean(rawPath)
	if filepath.IsAbs(path) {
		relative, err := filepath.Rel(repoRoot, path)
		if err == nil && !strings.HasPrefix(relative, "..") {
			return filepath.ToSlash(relative)
		}
		return ""
	}
	relative := strings.TrimLeft(filepath.ToSlash(path), "./")
	return relative
}

func shouldIgnoreRelativePath(relativePath string) bool {
	parts := []string{}
	for _, part := range strings.Split(filepath.ToSlash(relativePath), "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return false
	}
	excludedNames := map[string]struct{}{
		".git": {}, ".hg": {}, ".svn": {}, ".venv": {}, "venv": {}, "__pycache__": {}, ".mypy_cache": {}, ".pytest_cache": {}, ".ruff_cache": {}, ".turbo": {}, ".next": {}, "node_modules": {}, "dist": {}, "build": {}, "coverage": {}, ".coverage": {}, "tmp": {}, "vendor": {}, "third_party": {}, "research": {},
	}
	for _, part := range parts {
		if _, ok := excludedNames[part]; ok {
			return true
		}
	}
	normalized := strings.Join(parts, "/")
	for _, prefix := range []string{"backend/data", "frontend/dist"} {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+"/") {
			return true
		}
	}
	return false
}

func fileTimestamp(info os.FileInfo) string {
	return info.ModTime().UTC().Format(time.RFC3339Nano)
}

func shortPathHash(path string) string {
	sum := sha1.Sum([]byte(path))
	return hex.EncodeToString(sum[:])[:10]
}

func countIssuesFixed(items []issueRecord) int {
	total := 0
	for _, item := range items {
		if item.CodeStatus == "fixed" || item.DocStatus == "fixed" {
			total++
		}
	}
	return total
}

func countIssuesOpen(items []issueRecord) int {
	total := 0
	for _, item := range items {
		if item.IssueStatus == "open" || item.IssueStatus == "partial" {
			total++
		}
	}
	return total
}

func countReviewReady(items []issueRecord) int {
	total := 0
	for _, item := range items {
		if item.ReviewReadyCount > 0 {
			total++
		}
	}
	return total
}

func countReviewQueue(items []issueRecord) int {
	total := 0
	for _, item := range items {
		total += item.ReviewReadyCount
	}
	return total
}

func countSignalsPromoted(items []discoverySignal) int {
	total := 0
	for _, item := range items {
		if item.PromotedBugID != nil && strings.TrimSpace(*item.PromotedBugID) != "" {
			total++
		}
	}
	return total
}

func countDriftFlags(items []issueRecord) int {
	total := 0
	for _, item := range items {
		total += len(item.DriftFlags)
	}
	return total
}

func lastString(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[len(items)-1]
}

func firstNonEmpty(a string, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
