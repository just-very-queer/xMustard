package govstore

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

// TestMain runs the package under a governor with a small, fixed tree, so heavy-slot
// admission does not depend on what else runs on the host.
func TestMain(m *testing.M) {
	budget.Gov = quietGovernor(budget.GovernorConfig{})
	os.Exit(m.Run())
}

func quietGovernor(cfg budget.GovernorConfig) *budget.Governor {
	cfg.Sampler = func() (budget.TreeSample, error) {
		return budget.TreeSample{At: time.Now(), Supported: true, Basis: "test", Processes: 1, RSSBytes: 10 << 20, SelfRSSBytes: 10 << 20}, nil
	}
	cfg.FreeOSMemory = func() {}
	return budget.NewProcessGovernor(cfg)
}

// useGovernor installs g as the process governor for one test.
func useGovernor(t *testing.T, g *budget.Governor) {
	prev := budget.Gov
	budget.Gov = g
	t.Cleanup(func() { budget.Gov = prev })
}

func storeComponent(t *testing.T, g *budget.Governor) budget.ComponentStatus {
	t.Helper()
	for _, c := range g.Snapshot().Reservations.Components {
		if c.Name == "govstore" {
			return c
		}
	}
	t.Fatal("govstore is not a governed component")
	return budget.ComponentStatus{}
}

// An open store is a resident component that reserves its line; with every store
// closed it is listed but reserves nothing.
func TestOpenStoreIsAGovernedComponent(t *testing.T) {
	g := quietGovernor(budget.GovernorConfig{})
	if c := storeComponent(t, g); c.Enabled || c.Kind != budget.ComponentResident || c.Reclaimable {
		t.Fatalf("no store open: %+v", c)
	}
	s := openTestStore(t, nil)
	c := storeComponent(t, g)
	if !c.Enabled || c.ReservedSteadyBytes != storeSteadyBytes || c.ReservedPeakBytes != storePeakBytes {
		t.Fatalf("a store open: %+v", c)
	}
	_ = s.Close()
	_ = s.Close() // a second close must not release the reservation twice
	if c := storeComponent(t, g); c.Enabled {
		t.Fatalf("store closed: %+v", c)
	}
}

// gatedReader blocks its first read until release is closed, after signalling started.
type gatedReader struct {
	r                *strings.Reader
	started, release chan struct{}
	once             bool
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if !g.once {
		g.once = true
		close(g.started)
		<-g.release
	}
	return g.r.Read(p)
}

// A legacy import holds the heavy slot while it runs, and an import behind a busy slot
// waits the bound, is refused with ErrOverloaded and writes nothing.
func TestLegacyImportRunsInTheHeavySlot(t *testing.T) {
	g := quietGovernor(budget.GovernorConfig{HeavyWait: 200 * time.Millisecond})
	useGovernor(t, g)
	s := openTestStore(t, nil)
	ctx := context.Background()

	legacy := `[{"id":"ctx_1","workspace_id":"ws1","kind":"fact","title":"t","body":"b","author":"alice","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`
	gr := &gatedReader{r: strings.NewReader(legacy), started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := s.ImportContextEntriesJSON(ctx, "ws1", gr, ImportOptions{SkipInvalid: true})
		done <- err
	}()
	<-gr.started
	if h := g.Snapshot().HeavySlot; !h.Busy || h.Owner != "govstore:import/context_entries.json" || h.DeclaredBytes != importHeavyBytes {
		t.Fatalf("heavy slot during the import: %+v", h)
	}
	close(gr.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	release, err := g.AcquireHeavy(ctx, "test_holder", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	if _, err := s.ImportFeedbackJSON(ctx, "ws1", strings.NewReader(`[{"path":"a.go","retrieval_count":1}]`)); !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("an import behind a busy slot: want ErrOverloaded, got %v", err)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("refused after %s, before the wait bound", d)
	}
	if _, err := s.ImportContextEntriesJSON(budget.WithoutHeavyWait(ctx), "ws1", strings.NewReader("[]"), ImportOptions{}); !errors.Is(err, budget.ErrOverloaded) {
		t.Fatalf("a hot-path import: want ErrOverloaded at once, got %v", err)
	}
	var rows int
	if err := s.writer.QueryRowContext(ctx, `SELECT count(*) FROM path_feedback`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("a refused import wrote %d feedback rows (%v)", rows, err)
	}
}

// An Inline import (a source the caller sized as small) never takes the heavy slot, so
// it runs while other heavy work holds it.
func TestInlineImportSkipsTheHeavySlot(t *testing.T) {
	g := quietGovernor(budget.GovernorConfig{HeavyWait: 50 * time.Millisecond})
	useGovernor(t, g)
	s := openTestStore(t, nil)
	ctx := context.Background()
	release, err := g.AcquireHeavy(ctx, "test_holder", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	legacy := `[{"id":"ctx_1","workspace_id":"ws1","kind":"fact","title":"t","body":"b","author":"alice","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`
	if _, err := s.ImportContextEntriesJSON(ctx, "ws1", strings.NewReader(legacy), ImportOptions{SkipInvalid: true, Inline: true}); err != nil {
		t.Fatalf("an inline import behind a busy slot: %v", err)
	}
}
