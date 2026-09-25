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
// oracle are separated in these ways:
//
//  1. Oracle files never exist in the worktree while the agent runs. They are copied
//     in only after the agent's process group is dead, every process that carries the
//     run's marker or works inside the worktree has been killed, and the visible
//     verify step has been recorded. They are written from the bytes read when the
//     corpus was loaded, never re-read from disk, and the on-disk files are checked
//     against their load-time digests before staging.
//  2. Before the agent starts, the worktree is scanned for any file whose bytes equal
//     an oracle file and for any oracle destination that already exists; either fails
//     the run as a leaked oracle instead of producing a contaminated result.
//  3. Everything that executes code the agent could have written runs under the same
//     containment: the client, the setup, verify and oracle commands, and the xMustard
//     API (which runs git in the worktree). It denies reads and writes of the corpus
//     file, oracle sources, reference patches, the git directory (and every other
//     worktree copy) of any repository holding them, the eval output directory, the
//     harness's own git directories and other runs' worktrees: sandbox-exec on macOS,
//     bwrap on Linux when installed. The mode in effect is recorded per run; "none" is
//     reported as a warning.
//  4. Verify runs after the final snapshot, so the harness snapshots again afterwards;
//     when verify changed the tree, the worktree is restored to the agent's final tree
//     before the oracle is staged, and the run records it.
//  5. Oracle output is written to the run's artifact directory, never the worktree.

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

// check is one harness-run command.
type check struct {
	argv     []string
	dir      string
	env      map[string]string
	extraEnv []string // after env: the run marker
	sb       *sandbox // nil runs uncontained (validate, where no agent code exists)
	timeout  time.Duration
	logPath  string
	logRel   string
}

// run runs the command in its directory, writing combined output to logPath.
func (c check) run(ctx context.Context) CheckResult {
	res := CheckResult{Cmd: c.argv, ExitCode: -1, Log: c.logRel}
	lf, err := os.Create(c.logPath)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer lf.Close()
	bin, args, err := c.sb.wrap(c.argv[0], c.argv[1:])
	if err != nil {
		res.Error = err.Error()
		return res
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = c.dir
	cmd.Env = append(scrubbedEnv(), "PWD="+c.dir)
	for _, k := range sortedKeys(c.env) {
		cmd.Env = append(cmd.Env, k+"="+c.env[k])
	}
	cmd.Env = append(cmd.Env, c.extraEnv...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	if err := startGroup(cmd); err != nil {
		res.Error = err.Error()
		return res
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(c.timeout)
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
		b, err := c.fileBytes(f.Src)
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

// stageOracle writes the oracle files into the worktree from the bytes read when the
// corpus was loaded. Call only after every agent process is dead and the visible
// verify step has run.
func stageOracle(c *Corpus, t *Task, worktree string) error {
	for _, f := range t.Oracle.Files {
		b, err := c.fileBytes(f.Src)
		if err != nil {
			return err
		}
		dst := filepath.Join(worktree, filepath.FromSlash(f.Dest))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		// An agent may have created the destination; the oracle's copy wins.
		_ = os.RemoveAll(dst)
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// unstageOracle removes the oracle files again (validate reuses the worktree, and a
// kept worktree is left as the agent finished it).
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

// realPathOrParent resolves p, or (when p does not exist yet) its parent, so a rule
// can also cover a file the process might create.
func realPathOrParent(p string) (string, bool) {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real, true
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(parent, filepath.Base(p)), false
	}
	return "", false
}

// sandbox is the containment for one run. Every process that can execute code the
// agent wrote (the client, setup, verify and oracle commands, the xMustard API) is
// wrapped with it.
type sandbox struct {
	mode     string
	hidden   []string // neither readable nor writable
	readOnly []string // readable, not writable (may not exist yet)
	allow    []string // readable and writable although inside a hidden path
}

// with returns a copy that also allows the given paths.
func (s *sandbox) with(allow ...string) *sandbox {
	if s == nil {
		return nil
	}
	c := *s
	c.allow = append(slices.Clone(s.allow), allow...)
	return &c
}

// wrap returns the command line that runs bin with args under the sandbox.
func (s *sandbox) wrap(bin string, args []string) (string, []string, error) {
	if s == nil || s.mode == ContainNone || s.mode == "" {
		return bin, args, nil
	}
	hidden, allow := realPaths(s.hidden), realPaths(s.allow)
	switch s.mode {
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
		for _, p := range s.readOnly {
			if real, ok := realPathOrParent(p); real != "" {
				kind := "subpath" // covers a file or directory created later
				if ok {
					if fi, err := os.Stat(real); err == nil && !fi.IsDir() {
						kind = "literal"
					}
				}
				fmt.Fprintf(&rules, "(deny file-write* (%s %s))", kind, sbplString(real))
			}
		}
		// Later rules win: re-allow the process's own directories inside a hidden
		// path, plus metadata of their hidden ancestors so path resolution works.
		for _, p := range allow {
			fmt.Fprintf(&rules, "(allow file-read* file-write* (subpath %s))", sbplString(p))
			for a := filepath.Dir(p); a != filepath.Dir(a); a = filepath.Dir(a) {
				if slices.ContainsFunc(hidden, func(h string) bool { return a == h || isWithin(a, h) }) {
					fmt.Fprintf(&rules, "(allow file-read-metadata (literal %s))", sbplString(a))
				}
			}
		}
		return "sandbox-exec", append([]string{"-p", rules.String(), bin}, args...), nil
	case ContainBwrap:
		// A private pid namespace: every process the command starts, including ones
		// that leave its session (setsid), dies when the command's sandbox exits.
		out := []string{"--dev-bind", "/", "/", "--die-with-parent", "--unshare-pid", "--proc", "/proc"}
		for _, p := range hidden {
			fi, err := os.Stat(p)
			if err != nil {
				continue
			}
			if fi.IsDir() {
				out = append(out, "--tmpfs", p)
			} else {
				out = append(out, "--ro-bind", "/dev/null", p)
			}
		}
		for _, p := range realPaths(s.readOnly) {
			out = append(out, "--ro-bind", p, p)
		}
		for _, p := range allow {
			out = append(out, "--bind", p, p)
		}
		out = append(out, "--", bin)
		return "bwrap", append(out, args...), nil
	}
	return bin, args, fmt.Errorf("unknown containment %q", s.mode)
}

// contain wraps a client invocation with the sandbox.
func contain(sb *sandbox, inv Invocation) (Invocation, error) {
	bin, args, err := sb.wrap(inv.Bin, inv.Args)
	if err != nil {
		return inv, err
	}
	inv.Bin, inv.Args = bin, args
	return inv, nil
}

// sbplString quotes a path for a sandbox profile string literal.
func sbplString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// gitConfigPaths are the operator's global git config files. The agent may not write
// them: they would reach every later run and the operator.
func gitConfigPaths() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".gitconfig"), filepath.Join(home, ".config", "git"))
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		out = append(out, filepath.Join(x, "git"))
	}
	return out
}

// applyPatch applies a patch in dir (validate and the fake driver).
func applyPatch(dir string, patch []byte) error {
	cmd := exec.Command("git", "-c", "core.hooksPath=/dev/null", "apply", "--whitespace=nowarn", "-")
	cmd.Dir = dir
	cmd.Env = scrubbedEnv()
	cmd.Stdin = bytes.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
