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
// answer 400 invalid_id for on the next read of the object it just wrote.
func TestCreateIssueRefusesAnIdTheReadLayerWouldRefuse(t *testing.T) {
	cases := []struct {
		name  string
		bugID string
	}{
		{"parent traversal", "../../escape"},
		{"embedded traversal", "TRK_0001/../x"},
		{"nul byte", "TRK_0001\x00"},
		{"backslash", `TRK_0001\x`},
		{"control character", "TRK_0001\n"},
		{"over long", strings.Repeat("a", overLongID)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

			_, err := CreateIssue(dataDir, workspaceID, IssueCreateRequest{
				BugID:    stringPtr(c.bugID),
				Title:    "Traversal attempt",
				Severity: "p2",
			})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("CreateIssue(%q): want ErrInvalidInput, got %v", c.bugID, err)
			}
			assertIssueNotStored(t, dataDir, workspaceID, c.bugID)
		})
	}
}

// A view id is minted from the name through url.QueryEscape, which escapes "/" and
// "\" but leaves "." and an unbounded length alone. The name is refused here rather
// than an id the URL layer cannot carry being written.
func TestCreateSavedViewRefusesANameThatWouldMintAnUnusableId(t *testing.T) {
	cases := []struct {
		name string
		view string
	}{
		{"parent traversal", "../../escape"},
		{"dot segments", ".. .."},
		{"nul byte", "view\x00name"},
		{"over long", strings.Repeat("v", overLongID)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

			_, err := CreateSavedView(dataDir, workspaceID, SavedIssueViewRequest{
				Name:  c.view,
				Query: "export",
			})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("CreateSavedView(%q): want ErrInvalidInput, got %v", c.view, err)
			}
			assertNoViewsStored(t, dataDir, workspaceID)
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
// be carried by a path and hashed into an activity id.
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