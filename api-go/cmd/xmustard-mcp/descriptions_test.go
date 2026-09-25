package main

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
// to state their limits; they now total 1804. Lower this when one shrinks; do not raise it.
const toolDescriptionBudget = 1804

// Descriptions are an agent's only model of a tool. search, impact and recall must
// name the heuristics they run on, so an agent does not read a name-level fuzzy match
// as conceptual search, a lexical edge as a resolved call, or a shared path as a
// contradiction.
func TestToolDescriptionsStateImplementationLimits(t *testing.T) {
	want := map[string][]string{
		"search": {"symbol names", "not function bodies", "typo tolerance", "semantic-onnx", "rrf", "ast-grep"},
		"impact": {"lexical reference graph", "import lines", "leads to confirm, not proof"},
		"recall": {"conflicts", "path overlap, not contradiction"},
	}
	for name, phrases := range want {
		tl, ok := toolByName(name)
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
	search, _ := toolByName("search")
	if strings.Contains(search.Description, "lexical+semantic") {
		t.Errorf("search description still claims an unqualified semantic lane: %q", search.Description)
	}
}

func TestToolDescriptionsStayWithinBudget(t *testing.T) {
	total := 0
	for _, tl := range tools() {
		total += len(tl.Description)
	}
	if total > toolDescriptionBudget {
		t.Fatalf("tool descriptions total %d bytes, over the %d-byte budget", total, toolDescriptionBudget)
	}
}

// The Pi extension mirrors tools() verbatim; its live e2e diff only runs in the
// harness, so catch a description edited on one side here.
func TestPiMirrorCarriesGoDescriptions(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "integrations", "pi", "src", "tools.ts"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("integrations/pi not present in this checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools() {
		if !strings.Contains(string(src), `"`+tl.Description+`"`) {
			t.Errorf("integrations/pi/src/tools.ts does not carry the %s description verbatim", tl.Name)
		}
	}
}
