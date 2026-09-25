package workspaceops

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xmustard/api-go/internal/budget"
)

// Audit Go #5: provider responses were charged a 16 MiB reservation unconditionally.
// A provider read must be admitted against the pool as it streams, and refused with an
// explicit overload error rather than pushing the pool past its ceiling.
func TestProviderResponseIsAdmittedAgainstPool(t *testing.T) {
	prev := budget.TransientBytes
	pool := budget.NewByteBudget(1 << 20)
	budget.TransientBytes = pool
	defer func() { budget.TransientBytes = prev }()

	big := `{"data":[{"id":"` + strings.Repeat("m", 2<<20) + `"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if _, err := AddOpenAIProvider(dir, OpenAIProvider{Name: "p", Kind: "openai", BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	_, err := ListProviderModels(dir, "p")
	if pool.Peak() > pool.Max() {
		t.Fatalf("provider read drove pool peak %d past max %d", pool.Peak(), pool.Max())
	}
	if err == nil || !strings.Contains(err.Error(), "overload") {
		t.Fatalf("2 MiB provider response under a 1 MiB pool: want explicit overload error, got %v", err)
	}
	if pool.InUse() != 0 {
		t.Fatalf("reservation leaked: %d", pool.InUse())
	}
}
