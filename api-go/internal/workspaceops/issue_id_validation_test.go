package workspaceops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The id ceiling a create enforces, written as a literal so the test's expected
// values do not come from the constant under test. 128 is the ceiling
// transport.ValidWorkspaceID puts on a workspace pin.
const (
	idLenCeiling = 128
	overLongID   = 512
)

// A create must not mint an id the read layer then refuses. The read layer accepts a
// platform wildcard that is one safe segment, so each id below is one this API would
// answer 400 invalid_id for on the next read of the object it just wrote. The leading
// and trailing control characters are the whole id apart from a space, which the
// create trims; they must still be refused rather than normalized away.
func TestCreateIssueRefusesAnIdTheReadLayerWouldRefuse(t *testing.T) {
	cases := []struct {
		name  string
		bugID string
	}{
		{"parent traversal", "../../escape"},
		{"embedded traversal", "TRK_0001/../x"},
		{"nul byte", "TRK_0001\x00"},
		{"backslash", `TRK_0001\x`},
		{"embedded control character", "TRK_0001\n0002"},
		{"trailing control character", "TRK_0001\n"},
		{"leading control character", "\nTRK_0001"},
		{"a lone dot", "."},
		{"whitespace only", "\n\t "},
		{"over long", strings.Repeat("a", overLongID)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
			before := artifactState(t, dataDir, workspaceID)

			_, err := CreateIssue(dataDir, workspaceID, IssueCreateRequest{
				BugID:    stringPtr(c.bugID),
				Title:    "Traversal attempt",
				Severity: "p2",
			})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("CreateIssue(%q): want ErrInvalidInput, got %v", c.bugID, err)
			}
			assertIssueNotStored(t, dataDir, workspaceID, c.bugID)
			assertArtifactsUnchanged(t, before, dataDir, workspaceID)
		})
	}
}

// A view id is minted from the name through url.QueryEscape, which escapes "/" and
// "\" but leaves "." and an unbounded length alone, and turns a space into "+" and a
// literal "%" into "%25". The name is refused here rather than an id the URL layer
// cannot carry being written, so the percent-escaped characters are cases too: an id
// holding "%" reads back through a different value than it was stored under.
func TestCreateSavedViewRefusesANameThatWouldMintAnUnusableId(t *testing.T) {
	cases := []struct {
		name string
		view string
	}{
		{"parent traversal", "../../escape"},
		{"dot segments", ".. .."},
		{"nul byte", "view\x00name"},
		{"percent sign", "100% done"},
		{"ampersand", "wip & done"},
		{"over long", strings.Repeat("v", overLongID)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
			before := artifactState(t, dataDir, workspaceID)

			_, err := CreateSavedView(dataDir, workspaceID, SavedIssueViewRequest{
				Name:  c.view,
				Query: "export",
			})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("CreateSavedView(%q): want ErrInvalidInput, got %v", c.view, err)
			}
			assertNoViewsStored(t, dataDir, workspaceID)
			assertArtifactsUnchanged(t, before, dataDir, workspaceID)
		})
	}
}

// The ids other producers already mint must keep creating, so the guard refuses bad
// input rather than working input: the GitHub import's GH-<n>, a generated
// TRK_%04d, the ledger's P0_25M03_001 shape and a user-supplied BSS-42. The ledger
// shape ends in _007 because writeIssueContextFixture already holds _001.
func TestExistingIssueIdShapesStillCreate(t *testing.T) {
	for _, bugID := range []string{"GH-1234", "TRK_0007", "P0_25M03_007", "BSS-42"} {
		t.Run(bugID, func(t *testing.T) {
			dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

			created, err := CreateIssue(dataDir, workspaceID, IssueCreateRequest{
				BugID:    stringPtr(bugID),
				Title:    "Imported issue",
				Severity: "p2",
			})
			if err != nil {
				t.Fatalf("CreateIssue(%q): %v", bugID, err)
			}
			if created.BugID != bugID {
				t.Fatalf("bug id round trip: want %q, got %q", bugID, created.BugID)
			}
		})
	}
}

// A name that slugs cleanly keeps the readable slug it has always produced.
func TestCreateSavedViewKeepsANameThatSlugsCleanly(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	view, err := CreateSavedView(dataDir, workspaceID, SavedIssueViewRequest{
		Name:  "Agent Review",
		Query: "export",
	})
	if err != nil {
		t.Fatalf("create saved view: %v", err)
	}
	if view.Name != "Agent Review" {
		t.Fatalf("view name: want %q, got %q", "Agent Review", view.Name)
	}
	if !strings.HasPrefix(view.ViewID, "agent-review-") {
		t.Fatalf("view id slug: %q", view.ViewID)
	}
}

// The minted id is bounded at create, so a stored view id is always short enough to
// be carried by a path and hashed into an activity id. The length is asserted
// together with the charset and the ".." rule, because a ceiling that only checks
// length would still let an id the read layer refuses through.
func TestMintedViewIdStaysWithinTheLengthCeiling(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	view, err := CreateSavedView(dataDir, workspaceID, SavedIssueViewRequest{
		Name: strings.Repeat("v", idLenCeiling-16),
	})
	if err != nil {
		t.Fatalf("create saved view: %v", err)
	}
	if len(view.ViewID) > idLenCeiling {
		t.Fatalf("view id is %d bytes, over the %d ceiling", len(view.ViewID), idLenCeiling)
	}
	for _, r := range view.ViewID {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || r == '_' || r == '.' || r == '-') {
			t.Fatalf("minted view id %q holds %q, which the read layer refuses", view.ViewID, r)
		}
	}
	if strings.Contains(view.ViewID, "..") {
		t.Fatalf("minted view id %q holds a parent segment", view.ViewID)
	}
}

func assertIssueNotStored(t *testing.T, dataDir, workspaceID, bugID string) {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "tracker_issues.json"))
	if err == nil && strings.Contains(string(content), bugID) {
		t.Fatalf("refused bug id %q was written to tracker_issues.json", bugID)
	}
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("reload snapshot: %v", err)
	}
	for _, item := range snapshot.Issues {
		if item.BugID == bugID {
			t.Fatalf("refused bug id %q reached the snapshot", bugID)
		}
	}
}

func assertNoViewsStored(t *testing.T, dataDir, workspaceID string) {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(dataDir, "workspaces", workspaceID, "saved_views.json"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read saved views: %v", err)
	}
	if trimmed := strings.TrimSpace(string(content)); trimmed != "" && trimmed != "[]" && trimmed != "null" {
		t.Fatalf("a refused view was written to saved_views.json: %s", trimmed)
	}
}

// artifactState is every file a create can write: the tracker, the views, the
// workspace snapshot and the activity log. A refusal must leave all four byte for
// byte as they were, so the assertions compare against the pre-call contents rather
// than searching for the refused id: a write that omitted the id would still slip
// past a search.
//
// An absent file is recorded as a nil map entry, which is distinct from an existing
// empty one, so a refused create that creates an empty file is caught too.
func artifactState(t testing.TB, dataDir, workspaceID string) map[string]string {
	t.Helper()
	dir := filepath.Join(dataDir, "workspaces", workspaceID)
	state := map[string]string{}
	for _, name := range []string{
		"tracker_issues.json", "saved_views.json", "snapshot.json", "activity.jsonl",
	} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue // absent, and must still be absent
		}
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		state[name] = string(content)
	}
	return state
}

// assertArtifactsUnchanged fails when a refused create touched any artifact: a file
// that was absent before and exists now is a write, and so is any change in bytes.
func assertArtifactsUnchanged(t *testing.T, before map[string]string, dataDir, workspaceID string) {
	t.Helper()
	after := artifactState(t, dataDir, workspaceID)
	for name := range after {
		want, existed := before[name]
		if !existed {
			t.Fatalf("a refused create wrote %s (%d bytes)", name, len(after[name]))
		}
		if got := after[name]; got != want {
			t.Fatalf("a refused create rewrote %s", name)
		}
	}
	for name := range before {
		if _, exists := after[name]; !exists {
			t.Fatalf("a refused create removed %s", name)
		}
	}
}

// readArtifactState is artifactState for a caller that wants a verdict rather than
// a test failure; the refusal tables use artifactState, this one uses this.
func readArtifactState(dataDir, workspaceID string) map[string]string {
	dir := filepath.Join(dataDir, "workspaces", workspaceID)
	state := map[string]string{}
	for _, name := range []string{
		"tracker_issues.json", "saved_views.json", "snapshot.json", "activity.jsonl",
	} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			continue
		}
		state[name] = string(content)
	}
	return state
}

// artifactsUnchanged reports whether a refused create left every artifact alone,
// without failing the test. The refusal tables assert on the failure; this test
// needs the verdict the other way round, to prove the assertion fires at all.
func artifactsUnchanged(before map[string]string, dataDir, workspaceID string) (bool, string) {
	after := readArtifactState(dataDir, workspaceID)
	for name := range after {
		want, existed := before[name]
		if !existed {
			return false, "wrote " + name
		}
		if got := after[name]; got != want {
			return false, "rewrote " + name
		}
	}
	for name := range before {
		if _, exists := after[name]; !exists {
			return false, "removed " + name
		}
	}
	return true, ""
}

// The no-write assertion has to fail on the writes it is meant to catch, including
// an empty file, which the previous version recorded identically to an absent one.
// Each case writes an artifact the refusal is supposed to leave alone and requires
// the check to notice.
func TestArtifactsUnchangedCatchesAWrite(t *testing.T) {
	for _, name := range []string{
		"tracker_issues.json", "saved_views.json", "activity.jsonl",
	} {
		t.Run(name, func(t *testing.T) {
			dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)
			path := filepath.Join(dataDir, "workspaces", workspaceID, name)

			// An empty file where there was none: the case the old map[string]string
			// could not see, because it stored absence as the empty string.
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if ok, why := artifactsUnchanged(map[string]string{}, dataDir, workspaceID); ok {
				t.Fatalf("an empty %s was not caught as a write", name)
			} else {
				t.Logf("empty %s caught: %s", name, why)
			}

			// And a rewrite: same file, different bytes.
			before := artifactState(t, dataDir, workspaceID)
			if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if ok, why := artifactsUnchanged(before, dataDir, workspaceID); ok {
				t.Fatalf("a rewritten %s was not caught", name)
			} else {
				t.Logf("rewritten %s caught: %s", name, why)
			}

			// And a removal.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if ok, why := artifactsUnchanged(before, dataDir, workspaceID); ok {
				t.Fatalf("a removed %s was not caught", name)
			} else {
				t.Logf("removed %s caught: %s", name, why)
			}
		})
	}
}
