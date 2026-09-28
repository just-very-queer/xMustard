package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"xmustard/api-go/internal/rustcore"
)

func TestWithRefreshWorkReportsTheUpdateBehindARead(t *testing.T) {
	cov := json.RawMessage(`{"complete":true,"work":{"graph_cache":"hit","files_parsed":0,"lock":"not_used"}}`)
	var r indexRefresh
	if err := json.Unmarshal([]byte(`{"mode":"incremental","reason":"changes","counters":{"reparsed":1,"unchanged":41,"reused_from_cache":2,"bytes_read":900},"timing":{"elapsed_ms":120}}`), &r); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Complete bool           `json:"complete"`
		Work     map[string]any `json:"work"`
	}
	if err := json.Unmarshal(withRefreshWork(cov, &r), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.Work["graph_cache"] != "miss" || got.Work["files_parsed"] != float64(1) ||
		got.Work["files_reused"] != float64(43) || got.Work["bytes_parsed"] != float64(900) || got.Work["lock"] != "not_used" {
		t.Fatalf("refresh work not reported: %+v", got)
	}
	noop := indexRefresh{Mode: "noop"}
	if string(withRefreshWork(cov, &noop)) != string(cov) || string(withRefreshWork(cov, nil)) != string(cov) {
		t.Fatal("a no-op or absent refresh must leave the core's coverage as is")
	}
}

func TestCodeIndexRefreshRecordsOutcomePerIdentityKey(t *testing.T) {
	prev := codeIndexRunner
	t.Cleanup(func() { codeIndexRunner = prev })
	run := func(root, key string, out string, err error) *refreshFlight {
		codeIndexRunner = func(_ context.Context, gotRoot string, args ...string) ([]byte, error) {
			if gotRoot != root || len(args) != 2 || args[0] != "--identity-key" || args[1] != key {
				t.Fatalf("runner got %s %v", gotRoot, args)
			}
			return []byte(out), err
		}
		f := &refreshFlight{key: key, done: make(chan struct{})}
		runCodeIndexRefresh(root, f)
		<-f.done
		return f
	}
	state := func(root string) codeIndexRoot {
		codeIndex.Lock()
		defer codeIndex.Unlock()
		return *codeIndexState(root)
	}

	root := t.TempDir()
	if f := run(root, "k1", "", errors.New("index update failed")); f.report != nil {
		t.Fatal("a failed update has no report")
	}
	if st := state(root); st.failed != "k1" || st.indexed != "" || st.flight != nil {
		t.Fatalf("failed key not recorded: %+v", st)
	}
	if f := run(root, "k2", `{"mode":"full","reason":"no_index"}`, nil); f.report == nil || f.report.Mode != "full" {
		t.Fatalf("report not decoded: %+v", f.report)
	}
	if st := state(root); st.indexed != "k2" || st.failed != "" {
		t.Fatalf("successful key not recorded: %+v", st)
	}
	if f := run(root, "k3", `{}`, nil); f.report != nil {
		t.Fatal("an undecodable report counts as a failure")
	}
	if st := state(root); st.indexed != "k2" || st.failed != "k3" {
		t.Fatalf("an undecodable report must not replace the indexed key: %+v", st)
	}
}

func TestCodeIndexReadCarriesTheIdentityAndReportsTheRefresh(t *testing.T) {
	if got := (codeIndexRead{}).flags(); got != nil {
		t.Fatalf("no identity, no flag: %v", got)
	}
	if got := (codeIndexRead{key: "k1"}).flags(); len(got) != 1 || got[0] != "--identity-key=k1" {
		t.Fatalf("identity flag: %v", got)
	}
	out := []byte(`{"impacted":[],"coverage":{"work":{"graph_cache":"hit"}}}`)
	if got := (codeIndexRead{key: "k1"}).annotate(out); string(got) != string(out) {
		t.Fatalf("no refresh: result unchanged, got %s", got)
	}
	read := codeIndexRead{key: "k1", refresh: &indexRefresh{Mode: "incremental", Reason: "changes"}}
	var got struct {
		Impacted []any `json:"impacted"`
		Coverage struct {
			Work map[string]any `json:"work"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(read.annotate(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Impacted == nil || got.Coverage.Work["graph_cache"] != "miss" {
		t.Fatalf("refresh not reported on an impact result: %+v", got)
	}
}

// An update names the watcher's paths only when its batch accounts for every change;
// anything else checks the whole tree.
func TestRefreshArgsNameTheBatchPathsOnlyWhenTheBatchIsComplete(t *testing.T) {
	paths := []string{"a.go", "b/c.ts"}
	cases := []struct {
		name   string
		batch  *rustcore.RefreshBatch
		forced bool
		want   []string
	}{
		{"no watcher", nil, true, []string{"--identity-key", "k"}},
		{"a synced batch", &rustcore.RefreshBatch{Paths: paths, Synced: true}, false, []string{"--identity-key", "k", "--paths", "a.go", "b/c.ts"}},
		{"a Git state change only", &rustcore.RefreshBatch{Git: true, Synced: true}, false, []string{"--identity-key", "k", "--paths"}},
		{"a full refresh", &rustcore.RefreshBatch{Full: "ignore_file", Synced: true}, false, []string{"--identity-key", "k"}},
		{"an unsynced batch", &rustcore.RefreshBatch{Paths: paths}, false, []string{"--identity-key", "k"}},
		{"a read found nothing pending", &rustcore.RefreshBatch{Synced: true}, true, []string{"--identity-key", "k"}},
		{"a read took pending paths", &rustcore.RefreshBatch{Paths: paths, Synced: true}, true, []string{"--identity-key", "k", "--paths", "a.go", "b/c.ts"}},
	}
	for _, c := range cases {
		if got := refreshArgs("k", c.batch, c.forced); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if got := refreshArgs("", nil, false); len(got) != 0 {
		t.Errorf("no identity and no batch: %v", got)
	}
}
