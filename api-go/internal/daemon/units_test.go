package daemon

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testSpec has the characters unit formats treat specially in its paths and values.
func testSpec(t *testing.T) Spec {
	t.Helper()
	home := filepath.Join(t.TempDir(), `home dir & <x> 100%`)
	return Spec{
		Label:   "com.xmustard.test",
		UnitDir: filepath.Join(home, "units"),
		APIBin:  filepath.Join(home, "bin $HOME", "xmustard-api"),
		CoreBin: filepath.Join(home, "bin $HOME", "xmustard-core"),
		DataDir: filepath.Join(home, "data <dir>"),
		LogFile: filepath.Join(home, "logs", "api.log"),
		Port:    18042,
		Path:    "/usr/bin:/bin",
		Env:     map[string]string{"XMUSTARD_PROFILE": "platform", "XMUSTARD_NOTE": `a "b" \c %d $e`},
	}
}

// parsePlist decodes a property list into Go values: dict → map, array → []any,
// string, integer (int64 as string), true/false → bool.
func parsePlist(t *testing.T, doc string) map[string]any {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	var value func(start xml.StartElement) any
	value = func(start xml.StartElement) any {
		switch start.Name.Local {
		case "dict":
			m := map[string]any{}
			var key string
			for {
				tok, err := dec.Token()
				if err != nil {
					t.Fatalf("plist: %v", err)
				}
				switch el := tok.(type) {
				case xml.StartElement:
					if el.Name.Local == "key" {
						var k string
						_ = dec.DecodeElement(&k, &el)
						key = k
						continue
					}
					m[key] = value(el)
				case xml.EndElement:
					return m
				}
			}
		case "array":
			var a []any
			for {
				tok, err := dec.Token()
				if err != nil {
					t.Fatalf("plist: %v", err)
				}
				switch el := tok.(type) {
				case xml.StartElement:
					a = append(a, value(el))
				case xml.EndElement:
					return a
				}
			}
		case "true", "false":
			_ = dec.Skip()
			return start.Name.Local == "true"
		default: // string, integer
			var s string
			_ = dec.DecodeElement(&s, &start)
			return s
		}
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			t.Fatal("plist: no dict")
		}
		if err != nil {
			t.Fatalf("plist: %v", err)
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "dict" {
			return value(el).(map[string]any)
		}
	}
}

func TestLaunchdPlistIsAKeepAliveAgentWithoutCredentials(t *testing.T) {
	t.Setenv("XMUSTARD_APPROVER_TOKEN", "must-not-leak") // units never read the environment
	s := testSpec(t)
	files := Launchd{UID: 501}.Files(s)
	if len(files) != 1 || files[0].Path != filepath.Join(s.UnitDir, "com.xmustard.test.plist") {
		t.Fatalf("files = %+v", files)
	}
	doc := files[0].Content
	if !strings.Contains(doc, Marker) || strings.Contains(doc, "must-not-leak") {
		t.Fatalf("marker missing or a credential rendered:\n%s", doc)
	}
	p := parsePlist(t, doc)
	env := p["EnvironmentVariables"].(map[string]any)
	for k, want := range map[string]any{
		"Label":             "com.xmustard.test",
		"RunAtLoad":         true,
		"StandardErrorPath": s.LogFile,
		"StandardOutPath":   s.LogFile,
		"WorkingDirectory":  s.DataDir,
		"Umask":             "63",
		"ExitTimeOut":       "30",
	} {
		if p[k] != want {
			t.Errorf("%s = %v, want %v", k, p[k], want)
		}
	}
	if args := p["ProgramArguments"].([]any); len(args) != 1 || args[0] != s.APIBin {
		t.Errorf("ProgramArguments = %v", args)
	}
	if ka := p["KeepAlive"].(map[string]any); ka["SuccessfulExit"] != false {
		t.Errorf("KeepAlive = %v: restart after a failure only", ka)
	}
	for k, want := range map[string]string{
		"XMUSTARD_DATA_DIR": s.DataDir, "XMUSTARD_API_HOST": "127.0.0.1", "XMUSTARD_API_PORT": "18042",
		"XMUSTARD_LOG_FILE": s.LogFile, "XMUSTARD_CORE_BIN": s.CoreBin, "PATH": s.Path,
		"XMUSTARD_PROFILE": "platform", "XMUSTARD_NOTE": `a "b" \c %d $e`,
	} {
		if env[k] != want {
			t.Errorf("env %s = %v, want %q", k, env[k], want)
		}
	}
	if len(env) != 8 {
		t.Errorf("env has %d variables, want exactly the 8 set: %v", len(env), env)
	}
	if lint, err := exec.LookPath("plutil"); err == nil {
		path := filepath.Join(t.TempDir(), "agent.plist")
		_ = os.WriteFile(path, []byte(doc), 0o600)
		if out, err := exec.Command(lint, "-lint", path).CombinedOutput(); err != nil {
			t.Fatalf("plutil -lint: %v\n%s", err, out)
		}
	}
}

func TestSystemdUnitsQuoteValuesAndPassVerify(t *testing.T) {
	s := testSpec(t)
	files := Systemd{}.Files(s)
	if len(files) != 2 {
		t.Fatalf("files = %+v", files)
	}
	service, socket := files[0].Content, files[1].Content
	for _, want := range []string{
		Marker,
		"Requires=com.xmustard.test.socket",
		`ExecStart="` + strings.ReplaceAll(s.APIBin, "%", "%%") + `"`,
		"WorkingDirectory=" + strings.ReplaceAll(s.DataDir, "%", "%%"),
		`Environment="XMUSTARD_NOTE=a \"b\" \\c %%d $e"`,
		`Environment="XMUSTARD_API_HOST=127.0.0.1"`,
		"UnsetEnvironment=XMUSTARD_APPROVER_TOKEN XMUSTARD_API_TOKEN XMUSTARD_TOKEN XMUSTARD_AUTH_TOKENS",
		"Restart=on-failure",
		"TimeoutStopSec=30",
	} {
		if !strings.Contains(service, want) {
			t.Errorf("service lacks %q:\n%s", want, service)
		}
	}
	for _, want := range []string{Marker, "ListenStream=127.0.0.1:18042", "WantedBy=sockets.target"} {
		if !strings.Contains(socket, want) {
			t.Errorf("socket lacks %q:\n%s", want, socket)
		}
	}
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not installed; the rendering checks above ran")
	}
	// verify loads the units offline and checks every setting; ExecStart must exist
	s.UnitDir = t.TempDir()
	if err := os.MkdirAll(filepath.Dir(s.APIBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.APIBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range (Systemd{}).Files(s) {
		if err := os.WriteFile(f.Path, []byte(f.Content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, f.Path)
	}
	out, err := exec.Command(analyze, append([]string{"verify"}, paths...)...).CombinedOutput()
	if err != nil || strings.Contains(string(out), s.Label) {
		t.Fatalf("systemd-analyze verify: %v\n%s", err, out)
	}
	t.Logf("systemd-analyze verify: clean (%s)", strings.TrimSpace(string(out)))
}

func TestSpecValidateFailsClosed(t *testing.T) {
	good := testSpec(t)
	if err := good.Validate(); err != nil {
		t.Fatalf("good spec: %v", err)
	}
	for name, edit := range map[string]func(*Spec){
		"approver token":     func(s *Spec) { s.Env["XMUSTARD_APPROVER_TOKEN"] = "x" },
		"bearer tokens":      func(s *Spec) { s.Env["XMUSTARD_AUTH_TOKENS"] = "a:admin:b" },
		"any token":          func(s *Spec) { s.Env["GITHUB_TOKEN"] = "x" },
		"secret":             func(s *Spec) { s.Env["AWS_SECRET_ACCESS_KEY"] = "x" },
		"dsn with password":  func(s *Spec) { s.Env["XMUSTARD_PG_DSN"] = "postgres://u:p@h/db" },
		"lowercase token":    func(s *Spec) { s.Env["my_api_key"] = "x" },
		"setup's own host":   func(s *Spec) { s.Env["XMUSTARD_API_HOST"] = "0.0.0.0" },
		"setup's own path":   func(s *Spec) { s.Env["PATH"] = "/tmp" },
		"bad env name":       func(s *Spec) { s.Env["A-B"] = "x" },
		"newline in value":   func(s *Spec) { s.Env["XMUSTARD_NOTE"] = "a\nExecStart=/bin/evil" },
		"newline in path":    func(s *Spec) { s.DataDir += "\n[Service]" },
		"quote in a path":    func(s *Spec) { s.APIBin = `/opt/"x"/xmustard-api` },
		"backslash in path":  func(s *Spec) { s.LogFile = `/var/log\x.log` },
		"relative data dir":  func(s *Spec) { s.DataDir = "data" },
		"relative api":       func(s *Spec) { s.APIBin = "xmustard-api" },
		"label with a slash": func(s *Spec) { s.Label = "../evil" },
		"empty label":        func(s *Spec) { s.Label = "" },
		"port zero":          func(s *Spec) { s.Port = 0 },
		"port too big":       func(s *Spec) { s.Port = 70000 },
	} {
		s := testSpec(t)
		edit(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSearchPathKeepsAbsoluteDirsOnce(t *testing.T) {
	got := SearchPath("/usr/bin:.:bin:/opt/x:/usr/bin:", "/opt/x", "", "/srv/bin")
	if got != "/opt/x:/srv/bin:/usr/bin" {
		t.Fatalf("SearchPath = %q", got)
	}
}
