package workspaceops

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xmustard/api-go/internal/budget"
)

// Audit Go #5: provider responses were charged a 16 MiB reservation unconditionally.
// A provider read must be admitted against the pool as it streams: refused with an
// explicit overload error while other requests hold the pool, rather than pushing the
// pool past its ceiling, and refused permanently (not as an overload) when it could
// never fit the pool at all.
func TestProviderResponseIsAdmittedAgainstPool(t *testing.T) {
	prev := budget.TransientBytes
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

	pool := budget.NewByteBudget(4 << 20)
	budget.TransientBytes = pool
	other := budget.NewScope(pool)
	if err := other.Acquire(3 << 20); err != nil {
		t.Fatal(err)
	}
	_, err := ListProviderModels(dir, "p")
	if pool.Peak() > pool.Max() {
		t.Fatalf("provider read drove pool peak %d past max %d", pool.Peak(), pool.Max())
	}
	if err == nil || !strings.Contains(err.Error(), "overload") {
		t.Fatalf("2 MiB provider response with 1 MiB of the pool free: want explicit overload error, got %v", err)
	}
	if pool.InUse() != 3<<20 {
		t.Fatalf("reservation leaked: %d", pool.InUse()-3<<20)
	}
	other.Close()

	small := budget.NewByteBudget(1 << 20)
	budget.TransientBytes = small
	_, err = ListProviderModels(dir, "p")
	if err == nil || !errors.Is(err, budget.ErrTooLarge) || strings.Contains(err.Error(), "overload") {
		t.Fatalf("2 MiB provider response under an idle 1 MiB pool: want a permanent too-large error, got %v", err)
	}
	if small.Peak() > small.Max() || small.InUse() != 0 {
		t.Fatalf("pool peak %d (max %d), in use %d", small.Peak(), small.Max(), small.InUse())
	}
}
