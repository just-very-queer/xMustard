package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrForeignUnit: a unit file with the daemon's name exists but setup did not write it.
var ErrForeignUnit = errors.New("unit file was not written by xmustard-ops setup")

// Installer installs, drives and removes the daemon on one platform.
type Installer struct {
	Platform Platform
	Run      Runner
	Probe    func(context.Context, string) (Health, error)
	// Wait bounds how long Install and Restart wait for the daemon to answer.
	Wait time.Duration
}

// InstallReport says what Install did.
type InstallReport struct {
	Platform string   `json:"platform"`
	Label    string   `json:"label"`
	Units    []string `json:"units"`
	Changed  []string `json:"changed"`
	API      string   `json:"api"`
	DataDir  string   `json:"data_dir"`
	LogFile  string   `json:"log_file"`
	Health   Health   `json:"health"`
	Service  Status   `json:"service"`
}

// Install writes spec's units, (re)starts the daemon and waits until it answers its
// health probe. Running it again after an upgrade restarts the daemon on the new
// binaries, and the new daemon migrates the governance store as it starts. When the
// daemon does not answer, the unit files this call changed are put back (or removed,
// when there were none before) and the previous units are started again.
func (in Installer) Install(ctx context.Context, spec Spec) (InstallReport, error) {
	if err := spec.Validate(); err != nil {
		return InstallReport{}, err
	}
	files := in.Platform.Files(spec)
	prev, err := readOwned(files)
	if err != nil {
		return InstallReport{}, err
	}
	if len(prev) == 0 {
		// a first install must not take another API's answers for its own daemon's
		if _, err := in.Probe(ctx, spec.BaseURL()); err == nil {
			return InstallReport{}, fmt.Errorf("an xMustard API already answers on %s, and it is not one setup installed under %q; stop it or pick another --port", spec.BaseURL(), spec.Label)
		}
	}
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{spec.UnitDir, 0o755}, {spec.DataDir, 0o700}, {filepath.Dir(spec.LogFile), 0o700}} {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return InstallReport{}, err
		}
	}
	report := InstallReport{Platform: in.Platform.Name(), Label: spec.Label, API: spec.BaseURL(),
		DataDir: spec.DataDir, LogFile: spec.LogFile, Changed: []string{}}
	for _, f := range files {
		report.Units = append(report.Units, f.Path)
		if p := prev[f.Path]; p.exists && p.content == f.Content {
			continue
		}
		if err := writeFileAtomic(f.Path, f.Content); err != nil {
			return report, err
		}
		report.Changed = append(report.Changed, f.Path)
	}
	h, err := in.start(ctx, spec, report.Changed)
	if err != nil {
		if len(report.Changed) > 0 {
			undone, rbErr := in.rollback(ctx, spec, files, prev, report.Changed)
			if rbErr != nil {
				undone = "undoing the change failed: " + rbErr.Error()
			}
			err = fmt.Errorf("%w; %s", err, undone)
		}
		return report, fmt.Errorf("%w (daemon log: %s)", err, spec.LogFile)
	}
	report.Health = h
	report.Service = in.Platform.Status(ctx, in.Run, spec)
	return report, nil
}

// Restart (re)starts the installed daemon and waits until it answers.
func (in Installer) Restart(ctx context.Context, spec Spec) (Health, error) {
	return in.start(ctx, spec, nil)
}

// Stop stops the installed daemon so that nothing starts it before Restart.
func (in Installer) Stop(ctx context.Context, spec Spec) error {
	return in.Platform.Stop(ctx, in.Run, spec)
}

func (in Installer) start(ctx context.Context, spec Spec, changed []string) (Health, error) {
	if err := in.Platform.Start(ctx, in.Run, spec, changed); err != nil {
		return Health{}, err
	}
	h, err := WaitHealthy(ctx, in.Probe, spec.BaseURL(), in.Wait)
	if err != nil {
		return h, fmt.Errorf("the daemon did not answer %s within %s: %w", spec.BaseURL(), in.Wait, err)
	}
	return h, nil
}

// rollback puts back the unit files a failed install changed and starts them again,
// or removes the units when none existed before. It says which it did.
func (in Installer) rollback(ctx context.Context, spec Spec, files []File, prev map[string]unitState, changed []string) (string, error) {
	existed := false
	for _, f := range files {
		existed = existed || prev[f.Path].exists
	}
	if !existed {
		return "the new units were removed", in.Platform.Remove(ctx, in.Run, spec, func() error { return removeFiles(changed) })
	}
	for _, path := range changed {
		p := prev[path]
		restore := func() error { return writeFileAtomic(path, p.content) }
		if !p.exists {
			restore = func() error { return removeFiles([]string{path}) }
		}
		if err := restore(); err != nil {
			return "", err
		}
	}
	return "the previous units were put back", in.Platform.Start(ctx, in.Run, spec, changed)
}

// UninstallReport says what Uninstall removed.
type UninstallReport struct {
	Platform string   `json:"platform"`
	Label    string   `json:"label"`
	Removed  []string `json:"removed"`
}

// Uninstall stops the daemon and deletes its unit files. The data dir and the logs are
// kept, and a unit setup did not write is refused.
func (in Installer) Uninstall(ctx context.Context, spec Spec) (UninstallReport, error) {
	report := UninstallReport{Platform: in.Platform.Name(), Label: spec.Label, Removed: []string{}}
	present, err := in.Installed(spec)
	if err != nil || len(present) == 0 {
		return report, err
	}
	if err := in.Platform.Remove(ctx, in.Run, spec, func() error { return removeFiles(present) }); err != nil {
		return report, err
	}
	report.Removed = present
	return report, nil
}

// Installed lists the unit files of spec's label that exist, refusing any that setup
// did not write.
func (in Installer) Installed(spec Spec) ([]string, error) {
	if !labelPattern.MatchString(spec.Label) || !filepath.IsAbs(spec.UnitDir) {
		return nil, fmt.Errorf("label %q in unit dir %q: need a valid label and an absolute unit dir", spec.Label, spec.UnitDir)
	}
	files := in.Platform.Files(spec)
	prev, err := readOwned(files)
	if err != nil {
		return nil, err
	}
	var present []string
	for _, f := range files {
		if prev[f.Path].exists {
			present = append(present, f.Path)
		}
	}
	return present, nil
}

// StatusReport is the daemon's state as the service manager and the health probe see it.
type StatusReport struct {
	Platform    string   `json:"platform"`
	Label       string   `json:"label"`
	Units       []string `json:"units"`
	Service     Status   `json:"service"`
	API         string   `json:"api"`
	Health      *Health  `json:"health,omitempty"`
	HealthError string   `json:"health_error,omitempty"`
}

// Status reports the installed units, the service manager's state and one health
// probe (which, under socket activation, starts the daemon).
func (in Installer) Status(ctx context.Context, spec Spec) (StatusReport, error) {
	present, err := in.Installed(spec)
	if err != nil {
		return StatusReport{}, err
	}
	r := StatusReport{Platform: in.Platform.Name(), Label: spec.Label, Units: present, API: spec.BaseURL(),
		Service: in.Platform.Status(ctx, in.Run, spec)}
	if h, err := in.Probe(ctx, spec.BaseURL()); err != nil {
		r.HealthError = err.Error()
	} else {
		r.Health = &h
	}
	return r, nil
}

type unitState struct {
	content string
	exists  bool
}

// readOwned reads the current unit files and refuses one without Marker.
func readOwned(files []File) (map[string]unitState, error) {
	out := make(map[string]unitState, len(files))
	for _, f := range files {
		b, err := os.ReadFile(f.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !strings.Contains(string(b), Marker) {
			return nil, fmt.Errorf("%w: %s (move it away or pick another --label)", ErrForeignUnit, f.Path)
		}
		out[f.Path] = unitState{content: string(b), exists: true}
	}
	return out, nil
}

// writeFileAtomic replaces path through a temporary file in its directory, so a
// service manager never reads half a unit.
func writeFileAtomic(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	_, werr := tmp.WriteString(content)
	if err := errors.Join(werr, tmp.Chmod(0o644), tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func removeFiles(paths []string) error {
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
