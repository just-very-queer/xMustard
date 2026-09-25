package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/workspaceops"
)

func localDiagnosticsServer(t testing.TB, coreOnly bool) (http.Handler, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_DATA_DIR", dataDir)
	loaded, err := workspaceops.LoadWorkspace(dataDir, workspaceops.WorkspaceLoadRequest{RootPath: repo, AutoScan: true})
	if err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	encoded, _ := json.Marshal(loaded)
	var ws struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	_ = json.Unmarshal(encoded, &ws)
	mux := http.NewServeMux()
	registerRoutes(mux)
	return buildHandler(serverConfig{authMode: "off", coreOnly: coreOnly, dataDir: dataDir}, mux), ws.Workspace.WorkspaceID, repo
}

func do(t testing.TB, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCoreOnlyDiagnosticsGetWorksWithoutPostgres(t *testing.T) {
	h, ws, _ := localDiagnosticsServer(t, true)
	rec := do(t, h, "GET", "/api/workspaces/"+ws+"/diagnostics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("no-DSN GET: %d %s", rec.Code, rec.Body)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	storage, _ := body["storage"].(map[string]any)
	if storage["backend"] != "local" || storage["status"] != "no_baseline" || len(body["diagnostics"].([]any)) != 0 {
		t.Fatalf("unexpected no-baseline body: %s", rec.Body)
	}
	if _, ok := body["baseline"]; ok {
		t.Fatalf("baseline must be omitted: %s", rec.Body)
	}
	// CORE_ONLY stays GET-only for diagnostics.
	for _, path := range []string{"/diagnostics/status", "/diagnostics/run", "/diagnostics/live"} {
		method := "GET"
		if strings.HasSuffix(path, "/run") {
			method = "POST"
		}
		if rec := do(t, h, method, "/api/workspaces/"+ws+path, `{}`); rec.Code != http.StatusNotFound {
			t.Fatalf("CORE_ONLY must not expose %s: %d", path, rec.Code)
		}
	}
}

func TestPlatformDiagnosticsRunIsWorkspaceConfinedAndDeterministic(t *testing.T) {
	h, ws, repo := localDiagnosticsServer(t, false)
	base := "/api/workspaces/" + ws
	outside := filepath.Join(t.TempDir(), "d.json")
	_ = os.WriteFile(outside, []byte(`[]`), 0o644)
	_ = os.Symlink(outside, filepath.Join(repo, "link.json"))
	for _, p := range []string{outside, "../d.json", "link.json"} {
		rec := do(t, h, "POST", base+"/diagnostics/run", fmt.Sprintf(`{"input_path":%q}`, p))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("input %q: want 400, got %d %s", p, rec.Code, rec.Body)
		}
	}
	// A request body cannot grant the CLI's authority.
	if rec := do(t, h, "POST", base+"/diagnostics/run", fmt.Sprintf(`{"input_path":%q,"input_authority":1}`, outside)); rec.Code != http.StatusBadRequest {
		t.Fatalf("request JSON selected authority: %d", rec.Code)
	}
	fixture := filepath.Join(repo, ".xmustard-e2e")
	_ = os.MkdirAll(fixture, 0o755)
	_ = os.WriteFile(filepath.Join(fixture, "big.json"), []byte("["+strings.Repeat(" ", 1<<20)+"]"), 0o644)
	rec := do(t, h, "POST", base+"/diagnostics/run", `{"input_path":".xmustard-e2e/big.json"}`)
	if rec.Code != http.StatusRequestEntityTooLarge || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("cap breach: want 413 without Retry-After, got %d %v", rec.Code, rec.Header())
	}
	_ = os.WriteFile(filepath.Join(fixture, "d.json"), []byte(`[{"path":"src/app.go","message":"xm_local_diag","severity":1}]`), 0o644)
	rec = do(t, h, "POST", base+"/diagnostics/run", `{"input_path":".xmustard-e2e/d.json","source_kind":"compiler","source_name":"t"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"storage_backend":"local"`) {
		t.Fatalf("local run: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "GET", base+"/diagnostics", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "xm_local_diag") || !strings.Contains(rec.Body.String(), `"status":"available"`) {
		t.Fatalf("GET after run: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "GET", base+"/diagnostics/status", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"freshness_basis":"ingestion_identity"`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", base+"/diagnostics?diagnostic_run_id=diag_000000000000", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown run id: %d", rec.Code)
	}
}

func TestRespondDiagnosticsErrorSeparatesBusyFromCaps(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{fmt.Errorf("x: %w", workspaceops.ErrDiagnosticsLimit), http.StatusRequestEntityTooLarge},
		{fmt.Errorf("x: %w", budget.ErrAdmissionLimit), http.StatusRequestEntityTooLarge},
		{fmt.Errorf("x: %w", workspaceops.ErrDiagnosticsQuota), http.StatusConflict},
		{fmt.Errorf("x: %w", budget.ErrOverloaded), http.StatusServiceUnavailable},
		{fmt.Errorf("x: %w", workspaceops.ErrInvalidDiagnosticsRequest), http.StatusBadRequest},
		{os.ErrNotExist, http.StatusNotFound},
		{fmt.Errorf("x: %w", workspaceops.ErrDiagnosticsStoreCorrupt), http.StatusInternalServerError},
		{errors.New("boom"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		respondDiagnosticsError(rec, c.err)
		if rec.Code != c.code {
			t.Errorf("%v: want %d, got %d", c.err, c.code, rec.Code)
		}
		if retry := rec.Header().Get("Retry-After"); (c.code == http.StatusServiceUnavailable) != (retry != "") {
			t.Errorf("%v: Retry-After %q on %d", c.err, retry, rec.Code)
		}
	}
}
