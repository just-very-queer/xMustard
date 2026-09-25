package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// CorpusSchema is the only corpus schema version this executor accepts.
const CorpusSchema = "xmustard.eval/v1"

// Task classes group results in the report. Adding a class is a schema change.
var taskClasses = []string{"bugfix", "feature", "refactor", "localization", "memory_lifecycle", "reduction", "component"}

// Seeded-memory labels are the ground truth the lifecycle metrics (PAR-EVAL-02) score
// against. xMustard does not see the label; only the harness does.
const (
	LabelCurrent       = "current"       // a true, current fact the agent should be served
	LabelStale         = "stale"         // true when written; its paths are changed by drift
	LabelSuperseded    = "superseded"    // replaced by a later memory that names it in `supersedes`
	LabelDuplicate     = "duplicate"     // restates another memory (`duplicate_of`)
	LabelContradiction = "contradiction" // conflicts with another memory (`contradicts`)
	LabelForeignScope  = "foreign_scope" // seeded into a different workspace; serving it is a scope leak
	LabelPending       = "pending"       // proposed but never verified; serving it is a governance violation
)

var memoryLabels = []string{LabelCurrent, LabelStale, LabelSuperseded, LabelDuplicate, LabelContradiction, LabelForeignScope, LabelPending}

var taskIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Corpus is one task corpus file.
type Corpus struct {
	Schema      string   `yaml:"schema"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Defaults    Defaults `yaml:"defaults"`
	Tasks       []Task   `yaml:"tasks"`

	Path   string `yaml:"-"` // absolute path of the corpus file
	Dir    string `yaml:"-"` // directory relative paths resolve against
	SHA256 string `yaml:"-"` // digest of the corpus file bytes
}

// Defaults apply to every task that does not set its own value.
type Defaults struct {
	TimeoutSec int `yaml:"timeout_sec"`
	Repeats    int `yaml:"repeats"`
}

// Task is one unit of work run under every arm.
type Task struct {
	ID         string        `yaml:"id"`
	Class      string        `yaml:"class"`
	Repo       RepoSpec      `yaml:"repo"`
	Setup      []CommandSpec `yaml:"setup"`
	Prompt     string        `yaml:"prompt"`
	Verify     *CommandSpec  `yaml:"verify"`
	Oracle     OracleSpec    `yaml:"oracle"`
	Reference  *Reference    `yaml:"reference"`
	GoldFiles  []string      `yaml:"gold_files"`
	Memory     *MemorySpec   `yaml:"memory"`
	Arms       []string      `yaml:"arms"`
	TimeoutSec int           `yaml:"timeout_sec"`
}

// RepoSpec names the repository a task runs in: a fixture directory the harness turns
// into a throwaway git repository, or a local git repository at a pinned ref.
type RepoSpec struct {
	Fixture string `yaml:"fixture"`
	Path    string `yaml:"path"`
	Ref     string `yaml:"ref"`
}

// CommandSpec is an argv command (no shell) run with the worktree as its directory.
type CommandSpec struct {
	Cmd        []string          `yaml:"cmd"`
	TimeoutSec int               `yaml:"timeout_sec"`
	Env        map[string]string `yaml:"env"`
}

// OracleSpec is the hidden, harness-owned check that decides whether a run resolved
// the task. Its files enter the worktree only after the agent has exited.
type OracleSpec struct {
	Files      []OracleFile      `yaml:"files"`
	Cmd        []string          `yaml:"cmd"`
	TimeoutSec int               `yaml:"timeout_sec"`
	Env        map[string]string `yaml:"env"`
}

// OracleFile copies Src (relative to the corpus directory) to Dest (relative to the
// worktree root).
type OracleFile struct {
	Src  string `yaml:"src"`
	Dest string `yaml:"dest"`
}

// Reference is a known-good solution, used by `validate --oracles` and by the fake
// driver's dry runs.
type Reference struct {
	Patch string `yaml:"patch"`
}

// MemorySpec is the coding-memory fixture of a task (PAR-EVAL-02). Seeds are written
// through the real propose/verify API before the agent starts; drift edits are
// applied afterwards in every arm, so all arms start from the same files.
type MemorySpec struct {
	Seed  []SeedMemory `yaml:"seed"`
	Drift []DriftEdit  `yaml:"drift"`
}

// SeedMemory is one memory with its ground-truth label.
type SeedMemory struct {
	Key         string   `yaml:"key"`
	Label       string   `yaml:"label"`
	Title       string   `yaml:"title"`
	Content     string   `yaml:"content"`
	Paths       []string `yaml:"paths"`
	Supersedes  string   `yaml:"supersedes"`
	Contradicts string   `yaml:"contradicts"`
	DuplicateOf string   `yaml:"duplicate_of"`
}

// DriftEdit changes one file after seeding. Exactly one of Append, Replace or Delete.
type DriftEdit struct {
	Path    string  `yaml:"path"`
	Append  *string `yaml:"append"`
	Replace *string `yaml:"replace"`
	Delete  bool    `yaml:"delete"`
}

// LoadCorpus reads, strictly decodes and validates a corpus file.
func LoadCorpus(path string) (*Corpus, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	var c Corpus
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	c.Path = abs
	c.Dir = filepath.Dir(abs)
	sum := sha256.Sum256(raw)
	c.SHA256 = hex.EncodeToString(sum[:])
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate reports every schema problem at once.
func (c *Corpus) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if c.Schema != CorpusSchema {
		add("schema must be %q, got %q", CorpusSchema, c.Schema)
	}
	if strings.TrimSpace(c.Name) == "" {
		add("name is required")
	}
	if c.Defaults.TimeoutSec < 0 || c.Defaults.Repeats < 0 {
		add("defaults must not be negative")
	}
	if len(c.Tasks) == 0 {
		add("at least one task is required")
	}
	seen := map[string]bool{}
	for i := range c.Tasks {
		t := &c.Tasks[i]
		where := fmt.Sprintf("tasks[%d] (%s)", i, t.ID)
		if !taskIDPattern.MatchString(t.ID) {
			add("%s: id must match %s", where, taskIDPattern)
		}
		if seen[t.ID] {
			add("%s: duplicate id", where)
		}
		seen[t.ID] = true
		for _, e := range c.validateTask(t) {
			add("%s: %v", where, e)
		}
	}
	return errors.Join(errs...)
}

func (c *Corpus) validateTask(t *Task) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !slices.Contains(taskClasses, t.Class) {
		add("class must be one of %v", taskClasses)
	}
	switch {
	case t.Repo.Fixture != "" && t.Repo.Path != "":
		add("repo: set fixture or path, not both")
	case t.Repo.Fixture != "":
		if t.Repo.Ref != "" {
			add("repo.ref applies to path repos only")
		}
		if fi, err := os.Stat(c.resolve(t.Repo.Fixture)); err != nil || !fi.IsDir() {
			add("repo.fixture %q is not a directory", t.Repo.Fixture)
		}
	case t.Repo.Path != "":
		if strings.TrimSpace(t.Repo.Ref) == "" {
			add("repo.ref is required with repo.path (pin a commit)")
		}
		if fi, err := os.Stat(c.resolve(t.Repo.Path)); err != nil || !fi.IsDir() {
			add("repo.path %q is not a directory", t.Repo.Path)
		}
	default:
		add("repo: fixture or path is required")
	}
	if strings.TrimSpace(t.Prompt) == "" {
		add("prompt is required")
	}
	for j, s := range t.Setup {
		if len(s.Cmd) == 0 {
			add("setup[%d].cmd is required", j)
		}
	}
	if t.Verify != nil && len(t.Verify.Cmd) == 0 {
		add("verify.cmd is required when verify is set")
	}
	if len(t.Oracle.Cmd) == 0 {
		add("oracle.cmd is required: a run resolves only when the hidden oracle passes")
	}
	fixtureDir := ""
	if t.Repo.Fixture != "" {
		fixtureDir = c.resolve(t.Repo.Fixture)
	}
	for j, f := range t.Oracle.Files {
		src := c.resolve(f.Src)
		if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
			add("oracle.files[%d].src %q is not a regular file", j, f.Src)
		}
		if fixtureDir != "" && isWithin(src, fixtureDir) {
			add("oracle.files[%d].src is inside the fixture, so the agent would see it", j)
		}
		if !isCleanRelPath(f.Dest) {
			add("oracle.files[%d].dest %q must be a clean relative path", j, f.Dest)
		}
	}
	if t.Reference != nil {
		if fi, err := os.Stat(c.resolve(t.Reference.Patch)); err != nil || !fi.Mode().IsRegular() {
			add("reference.patch %q is not a regular file", t.Reference.Patch)
		}
	}
	for _, g := range t.GoldFiles {
		if !isCleanRelPath(g) {
			add("gold_files entry %q must be a clean relative path", g)
		}
	}
	for _, a := range t.Arms {
		if _, err := parseArm(a); err != nil {
			add("arms: %v", err)
		}
	}
	if t.TimeoutSec < 0 {
		add("timeout_sec must not be negative")
	}
	if t.Memory != nil {
		errs = append(errs, validateMemory(t.Memory)...)
	}
	return errs
}

func validateMemory(m *MemorySpec) []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	keys := map[string]SeedMemory{}
	for i, s := range m.Seed {
		if !taskIDPattern.MatchString(s.Key) {
			add("memory.seed[%d].key %q must match %s", i, s.Key, taskIDPattern)
		}
		if _, dup := keys[s.Key]; dup {
			add("memory.seed[%d]: duplicate key %q", i, s.Key)
		}
		keys[s.Key] = s
		if !slices.Contains(memoryLabels, s.Label) {
			add("memory.seed[%d].label must be one of %v", i, memoryLabels)
		}
		if strings.TrimSpace(s.Content) == "" {
			add("memory.seed[%d].content is required", i)
		}
		for _, p := range s.Paths {
			if !isCleanRelPath(p) {
				add("memory.seed[%d].paths entry %q must be a clean relative path", i, p)
			}
		}
	}
	drifted := map[string]bool{}
	for i, d := range m.Drift {
		if !isCleanRelPath(d.Path) {
			add("memory.drift[%d].path %q must be a clean relative path", i, d.Path)
		}
		n := 0
		if d.Append != nil {
			n++
		}
		if d.Replace != nil {
			n++
		}
		if d.Delete {
			n++
		}
		if n != 1 {
			add("memory.drift[%d]: set exactly one of append, replace, delete", i)
		}
		drifted[d.Path] = true
	}
	superseded := map[string]bool{}
	for _, s := range m.Seed {
		ref := func(field, key, wantLabel string) {
			if key == "" {
				return
			}
			other, ok := keys[key]
			switch {
			case !ok:
				add("memory %q: %s names unknown key %q", s.Key, field, key)
			case key == s.Key:
				add("memory %q: %s names itself", s.Key, field)
			case wantLabel != "" && other.Label != wantLabel:
				add("memory %q: %s target %q must be labeled %s", s.Key, field, key, wantLabel)
			}
		}
		ref("supersedes", s.Supersedes, LabelSuperseded)
		ref("contradicts", s.Contradicts, "")
		ref("duplicate_of", s.DuplicateOf, "")
		if s.Supersedes != "" {
			superseded[s.Supersedes] = true
		}
		switch s.Label {
		case LabelStale:
			hit := false
			for _, p := range s.Paths {
				hit = hit || drifted[p]
			}
			if !hit {
				add("memory %q: a stale memory needs paths changed by a drift edit", s.Key)
			}
		case LabelContradiction:
			if s.Contradicts == "" {
				add("memory %q: label contradiction needs contradicts", s.Key)
			}
		case LabelDuplicate:
			if s.DuplicateOf == "" {
				add("memory %q: label duplicate needs duplicate_of", s.Key)
			}
		}
	}
	for _, s := range m.Seed {
		if s.Label == LabelSuperseded && !superseded[s.Key] {
			add("memory %q: a superseded memory must be named by another memory's supersedes", s.Key)
		}
	}
	return errs
}

// resolve turns a corpus-relative path into an absolute one.
func (c *Corpus) resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(c.Dir, p)
}

// timeoutSec returns the task's agent timeout.
func (c *Corpus) timeoutSec(t *Task) int {
	switch {
	case t.TimeoutSec > 0:
		return t.TimeoutSec
	case c.Defaults.TimeoutSec > 0:
		return c.Defaults.TimeoutSec
	}
	return 900
}

// hiddenPaths are the paths an agent must never read: the corpus itself (it holds
// gold files and oracle commands), oracle sources, reference patches, and the
// original of every path repository (its later history holds the fix).
func (c *Corpus) hiddenPaths() []string {
	out := []string{c.Path}
	for i := range c.Tasks {
		t := &c.Tasks[i]
		if t.Repo.Path != "" {
			out = append(out, c.resolve(t.Repo.Path))
		}
		for _, f := range t.Oracle.Files {
			out = append(out, c.resolve(f.Src))
		}
		if t.Reference != nil {
			out = append(out, c.resolve(t.Reference.Patch))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func isCleanRelPath(p string) bool {
	if p == "" || filepath.IsAbs(p) || strings.Contains(p, `\`) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	return clean == p && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.HasPrefix(clean, ".git/") && clean != ".git"
}

func isWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// fixtureEpoch pins fixture commits so the same fixture always yields the same sha.
const fixtureEpoch = "2026-01-01T00:00:00Z"

// prepareRepo builds the scratch repository that per-run repositories borrow objects
// from, and returns it with the full sha of the task's starting commit. A fixture becomes
// a fresh single-commit repository with a pinned author and date. A path repository
// is fetched into a scratch repository with history only up to its ref, so an agent
// cannot find the future fix through `git log --all`.
func prepareRepo(c *Corpus, t *Task, cacheDir string) (repo, sha string, err error) {
	repo = filepath.Join(cacheDir, t.ID)
	if err := os.RemoveAll(repo); err != nil {
		return "", "", err
	}
	if t.Repo.Fixture != "" {
		if err := copyTree(c.resolve(t.Repo.Fixture), repo); err != nil {
			return "", "", fmt.Errorf("copy fixture: %w", err)
		}
		if _, err := git(repo, "init", "-q", "-b", "main"); err != nil {
			return "", "", err
		}
		if _, err := git(repo, "add", "-A"); err != nil {
			return "", "", err
		}
		if _, err := gitEnv(repo, []string{"GIT_AUTHOR_DATE=" + fixtureEpoch, "GIT_COMMITTER_DATE=" + fixtureEpoch},
			"-c", "user.name=xmustard-eval", "-c", "user.email=eval@xmustard.invalid", "-c", "commit.gpgsign=false",
			"commit", "-q", "--no-verify", "-m", "fixture "+t.ID); err != nil {
			return "", "", err
		}
		sha, err = git(repo, "rev-parse", "HEAD")
		return repo, sha, err
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return "", "", err
	}
	if _, err := git(repo, "init", "-q"); err != nil {
		return "", "", err
	}
	// --update-shallow lets a shallow clone (the usual pinned fixture) be the source.
	if _, err := git(repo, "fetch", "-q", "--no-tags", "--update-shallow", c.resolve(t.Repo.Path), t.Repo.Ref); err != nil {
		return "", "", fmt.Errorf("fetch %s at %s: %w", t.Repo.Path, t.Repo.Ref, err)
	}
	sha, err = git(repo, "rev-parse", "--verify", "-q", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("fetch %s at %s fetched no commit", t.Repo.Path, t.Repo.Ref)
	}
	// Keep the commit reachable by a ref, and drop FETCH_HEAD: it names the source
	// repository, whose later history the agent must not find.
	if _, err := git(repo, "update-ref", "refs/xmustard-eval/base", sha); err != nil {
		return "", "", err
	}
	if err := os.Remove(filepath.Join(repo, ".git", "FETCH_HEAD")); err != nil {
		return "", "", err
	}
	return repo, sha, nil
}

// copyTree copies regular files and directories (keeping the executable bit),
// skipping any .git directory. Symlinks are refused rather than followed.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, info.Mode().Perm()|0o600)
		default:
			return fmt.Errorf("%s: only regular files and directories are supported in fixtures", rel)
		}
	})
}

// git runs a git command in dir and returns its trimmed stdout. Hooks, CRLF
// conversion and the global excludes file are off, so results do not depend on the
// operator's git setup.
func git(dir string, args ...string) (string, error) {
	return gitEnv(dir, nil, args...)
}

func gitEnv(dir string, env []string, args ...string) (string, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.autocrlf=false", "-c", "core.excludesFile=/dev/null"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(scrubbedEnv(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// scrubbedEnv is the harness environment without any XMUSTARD_* variable, so neither
// the agent nor the task commands inherit the harness's own configuration.
func scrubbedEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "XMUSTARD_") {
			out = append(out, kv)
		}
	}
	return out
}
