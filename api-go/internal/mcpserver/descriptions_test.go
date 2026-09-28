package mcpserver

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// toolDescriptionBudget caps the bytes agents read in tools/list every session.
// The nine descriptions totalled 1808 bytes before search/impact/recall were rewritten
// to state their limits; they totalled 1794, and 1735 once search described BM25 (WS-18).
// Lower this when one shrinks; do not raise it.
const toolDescriptionBudget = 1735

// Descriptions are an agent's only model of a tool. search, impact and recall must
// name the heuristics they run on, so an agent does not read a name-level fuzzy match
// as conceptual search, a lexical edge as a resolved call, or a shared path as a
// contradiction.
func TestToolDescriptionsStateImplementationLimits(t *testing.T) {
	want := map[string][]string{
		// bodies are searched by BM25 over chunks (WS-18); the trigram lane is typo
		// tolerance, not meaning.
		"search": {"bm25", "function bodies", "doc sections", "typo tolerance (not meaning)", "rrf (k=60)", "ast-grep"},
		// symbol= is a file-level walk from the defining files, path= the same walk from
		// one file; from=&to= ignores direction.
		"impact": {"lexical reference graph", "import lines", "leads to confirm, not proof", "defining files", "path= → the same from one file", "undirected"},
		// paths alone gate results; no args ranks by working-tree overlap.
		"recall": {"conflicts", "path overlap, not contradiction", "non-matches dropped", "working-tree overlap"},
	}
	for name, phrases := range want {
		tl, ok := ToolByName(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		desc := strings.ToLower(tl.Description)
		for _, p := range phrases {
			if !strings.Contains(desc, p) {
				t.Errorf("%s description must state %q; got %q", name, p, tl.Description)
			}
		}
	}
	stale := map[string][]string{
		"search": {"lexical+semantic", "not function bodies", "semantic"},
		"impact": {"transitive dependents", "dependency path"},
		"recall": {"recency top-n"},
	}
	for name, phrases := range stale {
		tl, _ := ToolByName(name)
		for _, p := range phrases {
			if strings.Contains(strings.ToLower(tl.Description), p) {
				t.Errorf("%s description still claims %q: %q", name, p, tl.Description)
			}
		}
	}
}

func TestToolDescriptionsStayWithinBudget(t *testing.T) {
	total := 0
	for _, tl := range Tools() {
		total += len(tl.Description)
	}
	if total > toolDescriptionBudget {
		t.Fatalf("tool descriptions total %d bytes, over the %d-byte budget", total, toolDescriptionBudget)
	}
}

// The Pi extension mirrors Tools() verbatim; its live e2e diff only runs in the
// harness, so catch a description edited on one side here.
func TestPiMirrorCarriesGoDescriptions(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "integrations", "pi", "src", "tools.ts"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("integrations/pi not present in this checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range Tools() {
		if !strings.Contains(string(src), `"`+tl.Description+`"`) {
			t.Errorf("integrations/pi/src/tools.ts does not carry the %s description verbatim", tl.Name)
		}
	}
}
