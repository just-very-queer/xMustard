package daemon

import (
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Launchd is a macOS per-user agent in the gui/<uid> domain. launchd socket activation
// needs launch_activate_socket from libSystem, which a cgo-free Go binary cannot call,
// so the agent starts when it is loaded and at every login (RunAtLoad) and stays
// resident; KeepAlive restarts it after a crash or a failed exit, not after a clean
// stop.
type Launchd struct{ UID int }

// Name is the service manager's name.
func (Launchd) Name() string { return "launchd" }

// Defaults puts the agent, data and log under ~/Library.
func (Launchd) Defaults(_ func(string) string, home string) Spec {
	return Spec{
		Label:   "com.xmustard.api",
		UnitDir: filepath.Join(home, "Library", "LaunchAgents"),
		DataDir: filepath.Join(home, "Library", "Application Support", "xmustard"),
		LogFile: filepath.Join(home, "Library", "Logs", "xmustard", "api.log"),
		Port:    8042,
	}
}

func (Launchd) plistPath(s Spec) string { return filepath.Join(s.UnitDir, s.Label+".plist") }
func (l Launchd) domain() string        { return "gui/" + strconv.Itoa(l.UID) }
func (l Launchd) target(s Spec) string  { return l.domain() + "/" + s.Label }

// plistEntry is one key of a property-list dictionary. Its value is a string, an int,
// a bool, a []string (array) or a []plistEntry (dictionary).
type plistEntry struct {
	key   string
	value any
}

// Files renders the agent's property list.
func (l Launchd) Files(s Spec) []File {
	env := make([]plistEntry, 0, 8)
	for _, e := range s.Environ() {
		env = append(env, plistEntry{e.Key, e.Value})
	}
	doc := []plistEntry{
		{"Label", s.Label},
		{"ProgramArguments", []string{s.APIBin}},
		{"EnvironmentVariables", env},
		{"WorkingDirectory", s.DataDir},
		{"RunAtLoad", true},
		{"KeepAlive", []plistEntry{{"SuccessfulExit", false}}},
		{"ThrottleInterval", 10},
		// the daemon drains for up to 25 s (in-flight requests, then runs) before it exits
		{"ExitTimeOut", 30},
		{"Umask", 0o077},
		// the daemon moves stdout and stderr onto its rotated log as it starts; these
		// catch what comes before
		{"StandardOutPath", s.LogFile},
		{"StandardErrorPath", s.LogFile},
	}
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	fmt.Fprintf(&b, "<!-- %s: rerun setup instead of editing this file -->\n", Marker)
	b.WriteString("<plist version=\"1.0\">\n")
	writePlist(&b, 0, doc)
	b.WriteString("</plist>\n")
	return []File{{Path: l.plistPath(s), Content: b.String()}}
}

func writePlist(b *strings.Builder, depth int, v any) {
	indent := strings.Repeat("\t", depth)
	switch v := v.(type) {
	case string:
		b.WriteString(indent + "<string>")
		_ = xml.EscapeText(b, []byte(v))
		b.WriteString("</string>\n")
	case int:
		fmt.Fprintf(b, "%s<integer>%d</integer>\n", indent, v)
	case bool:
		fmt.Fprintf(b, "%s<%t/>\n", indent, v)
	case []string:
		b.WriteString(indent + "<array>\n")
		for _, s := range v {
			writePlist(b, depth+1, s)
		}
		b.WriteString(indent + "</array>\n")
	case []plistEntry:
		b.WriteString(indent + "<dict>\n")
		for _, e := range v {
			b.WriteString(indent + "\t<key>")
			_ = xml.EscapeText(b, []byte(e.key))
			b.WriteString("</key>\n")
			writePlist(b, depth+1, e.value)
		}
		b.WriteString(indent + "</dict>\n")
	default:
		panic(fmt.Sprintf("plist: unsupported value %T", v))
	}
}

// Start boots any loaded copy out, then bootstraps the agent, which starts it.
func (l Launchd) Start(ctx context.Context, run Runner, s Spec, _ []string) error {
	if err := l.Stop(ctx, run, s); err != nil {
		return err
	}
	// enable clears a disable left in launchd's override database, which would make
	// bootstrap fail
	for _, args := range [][]string{{"enable", l.target(s)}, {"bootstrap", l.domain(), l.plistPath(s)}} {
		if out, err := run(ctx, "launchctl", args...); err != nil {
			return fmt.Errorf("launchctl %s: %v: %s", args[0], err, strings.TrimSpace(out))
		}
	}
	return nil
}

// Stop boots the agent out of the user's domain and waits until launchd lets it go.
func (l Launchd) Stop(ctx context.Context, run Runner, s Spec) error {
	target := l.target(s)
	_, _ = run(ctx, "launchctl", "bootout", target) // fails when it is not loaded
	return waitFor(ctx, unloadWait, "launchd still has "+target+" loaded", func() bool {
		_, err := run(ctx, "launchctl", "print", target)
		return err != nil
	})
}

// Remove boots the agent out, then deletes its property list.
func (l Launchd) Remove(ctx context.Context, run Runner, s Spec, deleteFiles func() error) error {
	if err := l.Stop(ctx, run, s); err != nil {
		return err
	}
	return deleteFiles()
}

// Status reads `launchctl print`.
func (l Launchd) Status(ctx context.Context, run Runner, s Spec) Status {
	st := Status{Manager: l.Name(), State: "not-loaded"}
	out, err := run(ctx, "launchctl", "print", l.target(s))
	if err != nil {
		return st
	}
	f := fields(out, " = ")
	st.State = cmp.Or(f["state"], "loaded")
	st.PID, _ = strconv.Atoi(f["pid"])
	return st
}
