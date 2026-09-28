package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFileRotatesAtItsCapAndKeepsGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "api.log")
	l, err := OpenLog(path, 100, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err := fmt.Fprintf(l, "line %02d %s\n", i, strings.Repeat("x", 20)); err != nil { // 30 bytes
			t.Fatal(err)
		}
	}
	_ = l.Close()
	for _, p := range []string{path, path + ".1", path + ".2"} {
		fi, err := os.Stat(p)
		if err != nil || fi.Size() > 100 || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v: want a 0600 file within the cap", p, fi, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("more generations kept than asked")
	}
	live, _ := os.ReadFile(path)
	prev, _ := os.ReadFile(path + ".1")
	if !strings.HasSuffix(string(live), "line 19 "+strings.Repeat("x", 20)+"\n") || !strings.Contains(string(prev), "line 17") {
		t.Fatalf("newest lines must be live and the previous ones in .1:\nlive %q\n.1 %q", live, prev)
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("log dir mode %v", fi.Mode().Perm())
	}

	// a log already at its cap (a daemon restarted after a crash) rotates on open
	big := filepath.Join(t.TempDir(), "big.log")
	_ = os.WriteFile(big, []byte(strings.Repeat("y", 200)), 0o600)
	l, err = OpenLog(big, 100, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = l.Write([]byte("fresh\n"))
	_ = l.Close()
	if got, _ := os.ReadFile(big); string(got) != "fresh\n" {
		t.Fatalf("live after reopen = %q", got)
	}
	if fi, _ := os.Stat(big + ".1"); fi == nil || fi.Size() != 200 {
		t.Fatal("the over-cap log was not kept as .1")
	}
}

func TestLogConfig(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	if p, _, _, err := LogConfig(env(nil)); p != "" || err != nil {
		t.Fatalf("unset: %q %v", p, err)
	}
	p, maxBytes, keep, err := LogConfig(env(map[string]string{"XMUSTARD_LOG_FILE": "/var/log/x.log", "XMUSTARD_LOG_MAX_BYTES": "4096", "XMUSTARD_LOG_KEEP": "5"}))
	if p != "/var/log/x.log" || maxBytes != 4096 || keep != 5 || err != nil {
		t.Fatalf("set: %q %d %d %v", p, maxBytes, keep, err)
	}
	for _, bad := range []map[string]string{
		{"XMUSTARD_LOG_FILE": "relative.log"},
		{"XMUSTARD_LOG_FILE": "/x.log", "XMUSTARD_LOG_MAX_BYTES": "0"},
		{"XMUSTARD_LOG_FILE": "/x.log", "XMUSTARD_LOG_KEEP": "-1"},
		{"XMUSTARD_LOG_FILE": "/x.log", "XMUSTARD_LOG_MAX_BYTES": "10MB"},
	} {
		if _, _, _, err := LogConfig(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
