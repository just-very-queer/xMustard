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
//  1. Oracle files never enter the worktree. After the agent's process group is dead,
//     every process of the run the sweep can find has been killed, and the visible
//     verify step has run, the harness moves the run directory where no run's profile
//     reaches, copies it into a judge directory that every run's agent, setup and
//     verify profile hides (never following a link), resets the copy's worktree to the
//     agent's final snapshot, and stages the oracle there (see judgeSite). The oracle
//     runs under its own profile, which hides every run's worktree and allows only the
//     copy, at the worktree's own path: that path is a link to the copy while the
//     oracle runs, so absolute paths setup recorded resolve into the copy. A process
//     the run left behind that no sweep found keeps the profile it started under,
//     which allows the link's path but not the copy it resolves to, so it can reach
//     neither the copy nor any later run's directories. That profile pins the run
//     directory's own entry, so it cannot replace the link either.
//  2. Oracle files are written from the bytes read when the corpus was loaded, never
//     re-read from disk, and the on-disk files are checked against their load-time
//     digests before staging. Staging runs with the operator's rights, so it goes
//     through an os.Root and refuses a destination whose directories include a link.
//  3. Before the agent starts, the worktree is scanned for any file whose bytes equal
//     an oracle file and for any oracle destination that already exists; either fails
//     the run as a leaked oracle instead of producing a contaminated result.
//  4. Everything that executes code the agent could have written runs contained: the
//     client, the setup, verify and oracle commands, and the xMustard API (which runs
//     git in the worktree). The profiles deny reads and writes of the corpus file,
//     oracle sources, reference patches, the git directory (and every other worktree
//     copy) of any repository holding them, the eval output directory, the harness's
//     own git directories and judge copies, and every run's per-run directories except
//     the run's own: sandbox-exec on macOS, bwrap on Linux when installed. The mode in
//     effect is recorded per run; "none" is reported as a warning.
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
	track    *procSet // when set, receives every process seen in the command's tree
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
	if c.track != nil {
		// a process the command detaches is reparented away from its tree; recording
		// the tree while it runs lets the sweep find such a process again
		stopWatch := watchTree(cmd.Process.Pid, c.track, 100*time.Millisecond)
		defer stopWatch()
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

// unrunnableOracle explains an oracle exit status that usually means a command did not
// run at all, rather than that a check failed: 126 (found but not executable, or
// denied by containment) and 127 (not found). "" for any other status.
func unrunnableOracle(code int) string {
	switch code {
	case 126:
		return "the oracle exited 126: a command it ran could not be executed or was denied (is what setup installed reachable from the judged tree?)"
	case 127:
		return "the oracle exited 127: a command it ran was not found"
	}
	return ""
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

// stageOracle writes the oracle files into root (the judge copy, or validate's
// worktree) from the bytes read when the corpus was loaded. It runs with the
// operator's rights, outside any containment, and root holds a tree the agent wrote,
// so it never follows a link: a link among a destination's directories fails with
// oracle_path_symlink, an existing destination (whatever its type) is replaced, and
// every write goes through an os.Root, which cannot leave root.
func stageOracle(c *Corpus, t *Task, root string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range t.Oracle.Files {
		b, err := c.fileBytes(f.Src)
		if err != nil {
			return err
		}
		dest := filepath.FromSlash(f.Dest)
		if err := noLinkedDirs(r, filepath.Dir(dest)); err != nil {
			return err
		}
		if err := r.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := r.RemoveAll(dest); err != nil {
			return err
		}
		w, err := r.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// noLinkedDirs fails with oracle_path_symlink when dir (relative to r) or one of its
// ancestors inside r is a symbolic link.
func noLinkedDirs(r *os.Root, dir string) error {
	if dir == "." {
		return nil
	}
	p := ""
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := r.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("oracle_path_symlink: %s on the oracle's destination path is a symbolic link", filepath.ToSlash(p))
		}
	}
	return nil
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

// parentReal resolves every component of each path but the last, which may be a link
// or not exist yet.
func parentReal(paths []string) []string {
	var out []string
	for _, p := range paths {
		if parent, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			out = append(out, filepath.Join(parent, filepath.Base(p)))
		}
	}
	return out
}

// ownPaths is parentReal without the paths that do not exist or are links: allowing a
// link would allow wherever it points (while the oracle runs, a run directory's path
// leads to the judge copy).
func ownPaths(paths []string) []string {
	var out []string
	for _, p := range parentReal(paths) {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&fs.ModeSymlink == 0 {
			out = append(out, p)
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
	// pinned are allowed directories whose own entry the process may not create,
	// remove, rename or change (sandbox-exec; under bwrap every allowed directory is a
	// mount point, which cannot be replaced either)
	pinned []string
	// links inside a hidden path that the process may resolve, each to a directory in
	// allow: the judge site's link at the run directory's path
	links []string
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
	hidden, allow := realPaths(s.hidden), ownPaths(s.allow)
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
		hiddenAncestors := func(p string) {
			for a := filepath.Dir(p); a != filepath.Dir(a); a = filepath.Dir(a) {
				if slices.ContainsFunc(hidden, func(h string) bool { return a == h || isWithin(a, h) }) {
					fmt.Fprintf(&rules, "(allow file-read-metadata (literal %s))", sbplString(a))
				}
			}
		}
		for _, p := range allow {
			fmt.Fprintf(&rules, "(allow file-read* file-write* (subpath %s))", sbplString(p))
			hiddenAncestors(p)
		}
		for _, p := range parentReal(s.pinned) {
			fmt.Fprintf(&rules, "(deny file-write* (literal %s))", sbplString(p))
		}
		// A link itself only: what it resolves to is matched by its own path.
		for _, l := range parentReal(s.links) {
			fmt.Fprintf(&rules, "(allow file-read* (literal %s))", sbplString(l))
			hiddenAncestors(l)
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
		// the hidden area's tmpfs covers the link on disk; recreate it inside
		for _, l := range parentReal(s.links) {
			if fi, err := os.Lstat(l); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
				continue // not a link (yet): nothing to recreate
			}
			target, err := filepath.EvalSymlinks(l)
			if err != nil {
				return bin, args, fmt.Errorf("resolve %s: %w", l, err)
			}
			out = append(out, "--symlink", target, l)
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
