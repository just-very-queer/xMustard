package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Hidden-oracle isolation. The model-visible verify step and the harness-owned
// oracle are separated in four ways:
//
//  1. Oracle files never exist in the worktree while the agent runs. They are copied
//     in only after the agent's whole process group is dead and after the visible
//     verify step has been recorded, so neither can observe them.
//  2. Before the agent starts, the worktree is scanned for any file whose bytes equal
//     an oracle file and for any oracle destination that already exists; either fails
//     the run as a leaked oracle instead of producing a contaminated result.
//  3. The agent runs under containment that denies reads of the corpus file, oracle
//     sources, reference patches and the eval output directory (other runs' diffs and
//     oracle logs): sandbox-exec on macOS, bwrap on Linux when installed. The mode in
//     effect is recorded per run; "none" is reported as a warning.
//  4. Oracle output is written to the run's artifact directory, never the worktree.

// CheckResult is one harness-run command (setup, verify or oracle).
type CheckResult struct {
	Cmd        []string `json:"cmd"`
	Passed     bool     `json:"passed"`
	ExitCode   int      `json:"exit_code"`
	TimedOut   bool     `json:"timed_out,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	Log        string   `json:"log"` // artifact-relative path
	Error      string   `json:"error,omitempty"`
}

// runCheck runs argv in dir, writing combined output to logPath.
func runCheck(ctx context.Context, argv []string, dir string, env map[string]string, timeout time.Duration, logPath, logRel string) CheckResult {
	res := CheckResult{Cmd: argv, ExitCode: -1, Log: logRel}
	lf, err := os.Create(logPath)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer lf.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv()
	for _, k := range sortedKeys(env) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	if err := startGroup(cmd); err != nil {
		res.Error = err.Error()
		return res
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err = <-done:
	case <-timer.C:
		res.TimedOut = true
		killGroup(cmd.Process.Pid, 3*time.Second)
		err = <-done
	case <-ctx.Done():
		killGroup(cmd.Process.Pid, 3*time.Second)
		err = <-done
		res.Error = "interrupted"
	}
	killGroup(cmd.Process.Pid, time.Second)
	res.DurationMS = time.Since(start).Milliseconds()
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) && res.Error == "" {
		res.Error = err.Error()
	}
	res.Passed = err == nil && res.ExitCode == 0 && !res.TimedOut && res.Error == ""
	return res
}

func secondsOr(n, def int) time.Duration {
	if n > 0 {
		return time.Duration(n) * time.Second
	}
	return time.Duration(def) * time.Second
}

// scanForOracleLeaks fails when any oracle file's bytes already exist somewhere in
// the worktree, or when an oracle destination path already exists there.
func scanForOracleLeaks(c *Corpus, t *Task, worktree string) error {
	type digest struct {
		size int64
		sum  [32]byte
		src  string
	}
	var want []digest
	for _, f := range t.Oracle.Files {
		src := c.resolve(f.Src)
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		want = append(want, digest{int64(len(b)), sha256.Sum256(b), f.Src})
		if _, err := os.Lstat(filepath.Join(worktree, filepath.FromSlash(f.Dest))); err == nil {
			return fmt.Errorf("oracle destination %s already exists in the worktree before the agent runs", f.Dest)
		}
	}
	if len(want) == 0 {
		return nil
	}
	return filepath.WalkDir(worktree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		for _, w := range want {
			if info.Size() != w.size {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if sha256.Sum256(b) == w.sum {
				rel, _ := filepath.Rel(worktree, p)
				return fmt.Errorf("oracle file %s is visible to the agent at %s", w.src, filepath.ToSlash(rel))
			}
		}
		return nil
	})
}

// stageOracle copies the oracle files into the worktree. Call only after the agent's
// process group is dead and the visible verify step has run.
func stageOracle(c *Corpus, t *Task, worktree string) error {
	for _, f := range t.Oracle.Files {
		b, err := os.ReadFile(c.resolve(f.Src))
		if err != nil {
			return err
		}
		dst := filepath.Join(worktree, filepath.FromSlash(f.Dest))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		// An agent may have created the destination; the oracle's copy wins.
		_ = os.Remove(dst)
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// unstageOracle removes the oracle files again (validate reuses the worktree).
func unstageOracle(t *Task, worktree string) {
	for _, f := range t.Oracle.Files {
		_ = os.Remove(filepath.Join(worktree, filepath.FromSlash(f.Dest)))
	}
}

// ---- containment ----

// Containment modes.
const (
	ContainAuto    = "auto"
	ContainSandbox = "sandbox-exec"
	ContainBwrap   = "bwrap"
	ContainNone    = "none"
)

// resolveContainment turns "auto" into the strongest mode available here.
func resolveContainment(mode string) (string, error) {
	switch mode {
	case "", ContainAuto:
		if runtime.GOOS == "darwin" {
			if _, err := exec.LookPath("sandbox-exec"); err == nil {
				return ContainSandbox, nil
			}
		}
		if runtime.GOOS == "linux" {
			if _, err := exec.LookPath("bwrap"); err == nil {
				return ContainBwrap, nil
			}
		}
		return ContainNone, nil
	case ContainSandbox, ContainBwrap:
		if _, err := exec.LookPath(mode); err != nil {
			return "", fmt.Errorf("containment %s requested but not installed", mode)
		}
		return mode, nil
	case ContainNone:
		return mode, nil
	}
	return "", fmt.Errorf("unknown containment %q (auto, sandbox-exec, bwrap, none)", mode)
}

// realPaths resolves symlinks (macOS /tmp is /private/tmp, and sandbox rules match
// real paths) and drops paths that do not exist.
func realPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			out = append(out, real)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// contain wraps inv so the client can neither read nor write the hidden paths, and
// cannot write the read-only ones (the scratch repositories every run borrows from).
func contain(mode string, hidden, readOnly []string, inv Invocation) (Invocation, error) {
	hidden = realPaths(hidden)
	readOnly = realPaths(readOnly)
	switch mode {
	case ContainNone, "":
		return inv, nil
	case ContainSandbox:
		var rules strings.Builder
		rules.WriteString("(version 1)(allow default)")
		for _, p := range hidden {
			fi, err := os.Stat(p)
			if err != nil {
				continue
			}
			kind := "literal"
			if fi.IsDir() {
				kind = "subpath"
			}
			fmt.Fprintf(&rules, "(deny file-read* file-write* (%s %s))", kind, sbplString(p))
		}
		for _, p := range readOnly {
			fmt.Fprintf(&rules, "(deny file-write* (subpath %s))", sbplString(p))
		}
		inv.Args = append([]string{"-p", rules.String(), inv.Bin}, inv.Args...)
		inv.Bin = "sandbox-exec"
		return inv, nil
	case ContainBwrap:
		args := []string{"--dev-bind", "/", "/", "--die-with-parent"}
		for _, p := range hidden {
			fi, err := os.Stat(p)
			if err != nil {
				continue
			}
			if fi.IsDir() {
				args = append(args, "--tmpfs", p)
			} else {
				args = append(args, "--ro-bind", "/dev/null", p)
			}
		}
		for _, p := range readOnly {
			args = append(args, "--ro-bind", p, p)
		}
		args = append(args, "--", inv.Bin)
		inv.Args = append(args, inv.Args...)
		inv.Bin = "bwrap"
		return inv, nil
	}
	return inv, fmt.Errorf("unknown containment %q", mode)
}

// sbplString quotes a path for a sandbox profile string literal.
func sbplString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// applyPatch applies a reference patch in dir (validate and the fake driver).
func applyPatch(dir, patchPath string) error {
	b, err := os.ReadFile(patchPath)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "-c", "core.hooksPath=/dev/null", "apply", "--whitespace=nowarn", "-")
	cmd.Dir = dir
	cmd.Env = scrubbedEnv()
	cmd.Stdin = bytes.NewReader(b)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply %s: %w: %s", filepath.Base(patchPath), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
