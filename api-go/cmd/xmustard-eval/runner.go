package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Run status values. Only "completed" runs enter the paired statistics; the others
// are named in the report with their reason.
const (
	StatusCompleted   = "completed"
	StatusSkipped     = "skipped"
	StatusError       = "error" // harness failure; not the agent's outcome
	StatusInterrupted = "interrupted"
)

// RunSchema versions each runs.jsonl record.
const RunSchema = "xmustard.eval.run/v1"

// ClientConfig locates one client binary.
type ClientConfig struct {
	Bin          string   `yaml:"bin" json:"bin,omitempty"`
	Args         []string `yaml:"args" json:"args,omitempty"`
	Extension    string   `yaml:"extension" json:"extension,omitempty"` // pi: the xMustard Pi adapter entry
	MaxBudgetUSD float64  `yaml:"max_budget_usd" json:"max_budget_usd,omitempty"`
}

// ThresholdConfig is the pre-registered go/no-go bar handed to memory_harness.go's
// CompareArms. Numeric parity thresholds are defined in WS-50; these defaults only
// keep the verdict computable.
type ThresholdConfig struct {
	MaxP           float64 `yaml:"max_p" json:"max_p"`
	MinSolvedDelta float64 `yaml:"min_solved_delta" json:"min_solved_delta"`
	BootstrapIters int     `yaml:"bootstrap_iters" json:"bootstrap_iters"`
	Alpha          float64 `yaml:"alpha" json:"alpha"`
}

// RunConfig is one executor invocation (run-config YAML plus flags).
type RunConfig struct {
	Driver        string                  `yaml:"driver" json:"driver"`
	Model         string                  `yaml:"model" json:"model"`
	Arms          []string                `yaml:"arms" json:"arms"`
	ReferenceArm  string                  `yaml:"reference_arm" json:"reference_arm"`
	Repeats       int                     `yaml:"repeats" json:"repeats"`
	Seed          int64                   `yaml:"seed" json:"seed"`
	Containment   string                  `yaml:"containment" json:"containment"`
	Clients       map[string]ClientConfig `yaml:"clients" json:"clients,omitempty"`
	Stack         StackConfig             `yaml:"stack" json:"stack"`
	Hooks         map[string][]string     `yaml:"hooks" json:"hooks,omitempty"`
	Peers         []PeerConfig            `yaml:"peers" json:"peers,omitempty"`
	Pricing       map[string]Price        `yaml:"pricing" json:"pricing,omitempty"`
	Thresholds    ThresholdConfig         `yaml:"thresholds" json:"thresholds"`
	Tasks         []string                `yaml:"tasks" json:"tasks,omitempty"`
	FakeFailArms  []string                `yaml:"fake_fail_arms" json:"fake_fail_arms,omitempty"`
	KeepWorktrees bool                    `yaml:"keep_worktrees" json:"keep_worktrees,omitempty"`

	Fake bool `yaml:"-" json:"fake"`

	// resolved by prepare
	arms     []Arm
	self     string
	outDir   string
	workRoot string
	contain  string
	hidden   []string // paths the agent may neither read nor write
	// dry-run knobs (never read from YAML)
	fakeProbe   []string
	fakeSleepMS int
	fakeExit    int
}

func (c *RunConfig) peer(name string) (PeerConfig, bool) {
	for _, p := range c.Peers {
		if p.Name == name {
			return p, true
		}
	}
	return PeerConfig{}, false
}

func (c *RunConfig) client() ClientConfig { return c.Clients[c.Driver] }

// prepare validates the configuration and fills defaults.
func (c *RunConfig) prepare(corpus *Corpus, outDir string) error {
	var errs []error
	if kind, ok := strings.CutPrefix(c.Driver, "fake:"); ok {
		c.Fake, c.Driver = true, kind
	}
	if !slices.Contains(driverNames, c.Driver) {
		errs = append(errs, fmt.Errorf("driver must be one of %v (or fake:<driver> for a dry run)", driverNames))
	}
	if !c.Fake && strings.TrimSpace(c.Model) == "" {
		errs = append(errs, errors.New("a fixed --model is required for real-model runs"))
	}
	if len(c.Arms) == 0 {
		c.Arms = slices.Clone(DefaultArms)
	}
	c.arms = nil
	for _, a := range c.Arms {
		arm, err := parseArm(a)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		c.arms = append(c.arms, arm)
	}
	if c.ReferenceArm == "" {
		c.ReferenceArm = ArmBaseline
		if !slices.Contains(c.Arms, ArmBaseline) && len(c.Arms) > 0 {
			c.ReferenceArm = c.Arms[0]
		}
	}
	if !slices.Contains(c.Arms, c.ReferenceArm) {
		errs = append(errs, fmt.Errorf("reference_arm %q is not among the arms", c.ReferenceArm))
	}
	if c.Repeats <= 0 {
		c.Repeats = max(corpus.Defaults.Repeats, 1)
	}
	if c.Seed == 0 {
		c.Seed = 20260925
	}
	if c.Thresholds.MaxP == 0 {
		c.Thresholds.MaxP = 0.05
	}
	if c.Thresholds.BootstrapIters == 0 {
		c.Thresholds.BootstrapIters = 10000
	}
	if c.Thresholds.Alpha == 0 {
		c.Thresholds.Alpha = 0.05
	}
	for _, p := range c.Peers {
		if err := p.validate(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, id := range c.Tasks {
		if !slices.ContainsFunc(corpus.Tasks, func(t Task) bool { return t.ID == id }) {
			errs = append(errs, fmt.Errorf("task filter names unknown task %q", id))
		}
	}
	self, err := os.Executable()
	if err != nil {
		errs = append(errs, err)
	}
	c.self = self
	if c.Stack.Kind == "" {
		switch {
		case c.Stack.APIBin != "" || c.Stack.MCPBin != "":
			c.Stack.Kind = StackReal
		case c.Fake:
			c.Stack.Kind = StackStub
		default:
			c.Stack.Kind = StackNone
		}
	}
	switch c.Stack.Kind {
	case StackReal:
		if c.Stack.CoreBin == "" {
			c.Stack.CoreBin = os.Getenv("XMUSTARD_CORE_BIN")
		}
		for name, p := range map[string]*string{"api_bin": &c.Stack.APIBin, "mcp_bin": &c.Stack.MCPBin, "core_bin": &c.Stack.CoreBin} {
			if *p == "" {
				errs = append(errs, fmt.Errorf("stack.%s is required for the real stack", name))
				continue
			}
			abs, err := filepath.Abs(*p)
			if err == nil {
				_, err = os.Stat(abs)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("stack.%s: %v", name, err))
			}
			*p = abs
		}
	case StackStub:
		if !c.Fake {
			errs = append(errs, errors.New("the stub stack is for fake-driver dry runs only"))
		}
	case StackNone:
	default:
		errs = append(errs, fmt.Errorf("stack.kind must be real, stub or none"))
	}
	if c.Driver == "pi" && c.Fake && c.client().Extension == "" {
		// the fake agent emulates the adapter itself; the flag only marks the wiring
		if c.Clients == nil {
			c.Clients = map[string]ClientConfig{}
		}
		cc := c.client()
		cc.Extension = "fake-xmustard-pi-adapter"
		c.Clients["pi"] = cc
	}
	if c.Driver == "pi" && !c.Fake && c.Stack.Kind != StackNone && c.client().Extension == "" {
		errs = append(errs, errors.New("clients.pi.extension (integrations/pi/src/index.ts) is required for xMustard arms under pi"))
	}
	abs, err := filepath.Abs(outDir)
	if err != nil {
		errs = append(errs, err)
	}
	c.outDir = abs
	// Anything the sandboxed agent must execute or load cannot live in a hidden path.
	c.hidden = append(corpus.hiddenPaths(), c.outDir)
	for _, p := range []string{c.Stack.MCPBin, c.client().Extension, c.client().Bin} {
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		for _, h := range c.hidden {
			if p == h || isWithin(p, h) {
				errs = append(errs, fmt.Errorf("%s is inside %s, which the agent cannot read", p, h))
			}
		}
	}
	mode, err := resolveContainment(c.Containment)
	if err != nil {
		errs = append(errs, err)
	}
	c.contain = mode
	return errors.Join(errs...)
}

// Isolation records what the harness enforced for one run.
type Isolation struct {
	WorktreeDetached bool   `json:"worktree_detached"`
	OracleLeakScan   string `json:"oracle_leak_scan"` // clean | leaked | skipped
	OracleStagedPost bool   `json:"oracle_staged_after_agent"`
	Containment      string `json:"containment"`
	WorktreeRemoved  bool   `json:"worktree_removed"`
}

// RunRecord is one line of runs.jsonl.
type RunRecord struct {
	Schema       string         `json:"schema"`
	TaskID       string         `json:"task_id"`
	TaskClass    string         `json:"task_class"`
	Arm          string         `json:"arm"`
	Rep          int            `json:"rep"`
	Driver       string         `json:"driver"`
	Fake         bool           `json:"fake,omitempty"`
	Model        string         `json:"model,omitempty"`
	RepoSHA      string         `json:"repo_sha,omitempty"`
	Status       string         `json:"status"`
	Reason       string         `json:"reason,omitempty"`
	Resolved     bool           `json:"resolved"`
	PromptSHA256 string         `json:"prompt_sha256,omitempty"`
	Client       *ClientExit    `json:"client,omitempty"`
	Transcript   *Transcript    `json:"transcript,omitempty"`
	Setup        []CheckResult  `json:"setup,omitempty"`
	Verify       *CheckResult   `json:"verify,omitempty"`
	Oracle       *CheckResult   `json:"oracle,omitempty"`
	Churn        *DiffChurn     `json:"churn,omitempty"`
	Localization *Localization  `json:"localization,omitempty"`
	RSS          *RSSSummary    `json:"rss,omitempty"`
	Stack        *StackInfo     `json:"stack,omitempty"`
	Memory       *MemoryMetrics `json:"memory,omitempty"`
	Isolation    *Isolation     `json:"isolation,omitempty"`
	Artifacts    string         `json:"artifacts,omitempty"`
	StartedAt    string         `json:"started_at,omitempty"`
	FinishedAt   string         `json:"finished_at,omitempty"`
}

// Manifest (eval.json) binds a result set to what produced it.
type Manifest struct {
	Schema         string            `json:"schema"`
	StartedAt      string            `json:"started_at"`
	FinishedAt     string            `json:"finished_at,omitempty"`
	Interrupted    bool              `json:"interrupted,omitempty"`
	Corpus         string            `json:"corpus"`
	CorpusName     string            `json:"corpus_name"`
	CorpusSHA256   string            `json:"corpus_sha256"`
	Config         RunConfig         `json:"config"`
	Containment    string            `json:"containment"`
	ClientVersion  string            `json:"client_version"`
	CodexServers   []string          `json:"codex_user_mcp_servers,omitempty"`
	StackBinaries  map[string]string `json:"stack_binaries_sha256,omitempty"`
	Host           map[string]any    `json:"host"`
	DryRun         bool              `json:"dry_run"`
	ExecutorBinary string            `json:"executor_sha256,omitempty"`
	// ExecutorMaxRSS is the executor's own peak RSS, a separate line: it is not part
	// of the measured xMustard tree.
	ExecutorMaxRSS int64 `json:"executor_max_rss_bytes,omitempty"`
}

const manifestSchema = "xmustard.eval.manifest/v1"

// executor carries the per-invocation state runOne needs.
type executor struct {
	cfg         *RunConfig
	corpus      *Corpus
	driver      Driver
	registry    *worktreeRegistry
	userServers []string
	runsFile    *os.File
}

// Execute runs every selected task under every arm, writes runs.jsonl, eval.json and
// the report into cfg.outDir, and returns ctx's error when interrupted.
func Execute(ctx context.Context, cfg *RunConfig, corpus *Corpus) (*Report, error) {
	if err := os.MkdirAll(cfg.outDir, 0o755); err != nil {
		return nil, err
	}
	runsPath := filepath.Join(cfg.outDir, "runs.jsonl")
	if _, err := os.Stat(runsPath); err == nil {
		return nil, fmt.Errorf("%s already exists; use a fresh --out directory", runsPath)
	}
	workRoot, err := os.MkdirTemp("", "xmustard-eval-")
	if err != nil {
		return nil, err
	}
	cfg.workRoot = workRoot
	defer func() {
		if !cfg.KeepWorktrees {
			_ = os.RemoveAll(workRoot)
		}
	}()
	d, err := driverByName(cfg.Driver)
	if err != nil {
		return nil, err
	}
	ex := &executor{cfg: cfg, corpus: corpus, driver: d, registry: newWorktreeRegistry()}
	defer func() { _ = ex.registry.removeAll() }()

	m := &Manifest{Schema: manifestSchema, StartedAt: nowUTC(), Corpus: corpus.Path, CorpusName: corpus.Name,
		CorpusSHA256: corpus.SHA256, Config: *cfg, Containment: cfg.contain, DryRun: cfg.Fake || cfg.Stack.Kind == StackStub,
		Host: map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU(), "go": runtime.Version()}}
	m.Config.Peers = nil
	for _, p := range cfg.Peers {
		p.Env = nil // may hold credentials
		m.Config.Peers = append(m.Config.Peers, p)
	}
	m.ExecutorBinary, _ = fileSHA256(cfg.self)
	if cfg.Fake {
		m.ClientVersion = "fake:" + cfg.Driver
	} else {
		m.ClientVersion = clientVersion(binOr(cfg.client(), cfg.Driver))
		if cfg.Driver == "codex" {
			if ex.userServers, err = codexUserServers(binOr(cfg.client(), "codex")); err != nil {
				return nil, err
			}
			m.CodexServers = ex.userServers
		}
	}
	if cfg.Stack.Kind == StackReal {
		m.StackBinaries = map[string]string{}
		for name, p := range map[string]string{"xmustard-api": cfg.Stack.APIBin, "xmustard-mcp": cfg.Stack.MCPBin, "xmustard-core": cfg.Stack.CoreBin} {
			m.StackBinaries[name], _ = fileSHA256(p)
		}
	}
	if err := writeJSONFile(filepath.Join(cfg.outDir, "eval.json"), m); err != nil {
		return nil, err
	}

	ex.runsFile, err = os.Create(runsPath)
	if err != nil {
		return nil, err
	}
	defer ex.runsFile.Close()

	type source struct {
		repo, sha, err string
	}
	repos := map[string]source{}
	var tasks []*Task
	for i := range corpus.Tasks {
		t := &corpus.Tasks[i]
		if len(cfg.Tasks) > 0 && !slices.Contains(cfg.Tasks, t.ID) {
			continue
		}
		tasks = append(tasks, t)
		repo, sha, err := prepareRepo(corpus, t, filepath.Join(workRoot, "repos"))
		p := source{repo: repo, sha: sha}
		if err != nil {
			p.err = err.Error()
		}
		repos[t.ID] = p
	}

	for rep := 0; rep < cfg.Repeats && ctx.Err() == nil; rep++ {
		for _, t := range tasks {
			if ctx.Err() != nil {
				break
			}
			for _, arm := range armOrder(cfg.arms, cfg.Seed, t.ID, rep) {
				if ctx.Err() != nil {
					break
				}
				p := repos[t.ID]
				var rec RunRecord
				if p.err != "" {
					rec = ex.baseRecord(t, arm, rep, p.sha)
					rec.Status, rec.Reason = StatusError, "prepare repository: "+p.err
				} else {
					rec = ex.runOne(ctx, t, arm, rep, p.repo, p.sha)
				}
				if err := ex.appendRecord(rec); err != nil {
					return nil, err
				}
			}
		}
	}
	m.FinishedAt = nowUTC()
	m.Interrupted = ctx.Err() != nil
	m.ExecutorMaxRSS = selfMaxRSSBytes()
	if err := writeJSONFile(filepath.Join(cfg.outDir, "eval.json"), m); err != nil {
		return nil, err
	}
	rep, err := writeReport(cfg.outDir)
	if err != nil {
		return nil, err
	}
	if m.Interrupted {
		return rep, ctx.Err()
	}
	return rep, nil
}

// armOrder shuffles the arms per task and repetition with a seeded generator, so no
// arm systematically runs first (warm caches) while the order stays reproducible.
func armOrder(arms []Arm, seed int64, taskID string, rep int) []Arm {
	h := fnv.New64a()
	_, _ = io.WriteString(h, taskID+"#"+strconv.Itoa(rep))
	rng := rand.New(rand.NewSource(seed ^ int64(h.Sum64())))
	out := slices.Clone(arms)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func (ex *executor) appendRecord(rec RunRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := ex.runsFile.Write(append(b, '\n')); err != nil {
		return err
	}
	return ex.runsFile.Sync()
}

func (ex *executor) baseRecord(t *Task, arm Arm, rep int, sha string) RunRecord {
	return RunRecord{Schema: RunSchema, TaskID: t.ID, TaskClass: t.Class, Arm: arm.Name, Rep: rep,
		Driver: ex.cfg.Driver, Fake: ex.cfg.Fake, Model: ex.cfg.Model, RepoSHA: sha}
}

func armDir(name string) string { return strings.ReplaceAll(name, ":", "-") }

func opaqueRunID(task, arm string, rep int) string {
	h := sha256.Sum256([]byte(task + "\x00" + arm + "\x00" + strconv.Itoa(rep)))
	return hex.EncodeToString(h[:6])
}

// repoDirName is the worktree's directory name: the fixture's or repository's own.
func repoDirName(c *Corpus, t *Task) string {
	src := t.Repo.Fixture
	if src == "" {
		src = t.Repo.Path
	}
	if name := filepath.Base(c.resolve(src)); name != "." && name != string(filepath.Separator) {
		return name
	}
	return "repo"
}

// runOne executes one (task, arm, repetition) in a fresh detached worktree.
func (ex *executor) runOne(ctx context.Context, t *Task, arm Arm, rep int, repo, sha string) (rec RunRecord) {
	cfg, corpus := ex.cfg, ex.corpus
	rec = ex.baseRecord(t, arm, rep, sha)
	if reason := armSkipReason(arm, t, cfg); reason != "" {
		rec.Status, rec.Reason = StatusSkipped, reason
		return rec
	}
	rec.StartedAt = nowUTC()
	defer func() { rec.FinishedAt = nowUTC() }()
	rel := filepath.Join("runs", t.ID, armDir(arm.Name), "r"+strconv.Itoa(rep))
	art := filepath.Join(cfg.outDir, rel)
	rec.Artifacts = filepath.ToSlash(rel)
	fail := func(status, reason string) RunRecord {
		rec.Status, rec.Reason = status, reason
		if ctx.Err() != nil {
			rec.Status = StatusInterrupted
		}
		return rec
	}
	if err := os.MkdirAll(art, 0o755); err != nil {
		return fail(StatusError, err.Error())
	}
	iso := &Isolation{Containment: cfg.contain, OracleLeakScan: "skipped"}
	rec.Isolation = iso

	// Paths the agent can see (its working directory, the workspace id derived from
	// it, `git worktree list`) must not reveal the arm: an opaque run id plus the
	// repository's own name, identical across arms.
	runID := opaqueRunID(t.ID, arm.Name, rep)
	wt, err := newRunWorktree(repo, sha, filepath.Join(cfg.workRoot, "runrepos", runID), filepath.Join(cfg.workRoot, "wt", runID, repoDirName(corpus, t)))
	if err != nil {
		return fail(StatusError, "create worktree: "+err.Error())
	}
	ex.registry.add(wt)
	iso.WorktreeDetached = true
	defer func() {
		if cfg.KeepWorktrees {
			return
		}
		if err := ex.registry.remove(wt); err != nil && rec.Reason == "" {
			rec.Reason = "worktree cleanup: " + err.Error()
		}
		iso.WorktreeRemoved = wt.gone()
	}()

	for i, s := range t.Setup {
		res := runCheck(ctx, s.Cmd, wt.Dir, s.Env, secondsOr(s.TimeoutSec, 600), filepath.Join(art, fmt.Sprintf("setup-%d.log", i)), fmt.Sprintf("setup-%d.log", i))
		rec.Setup = append(rec.Setup, res)
		if !res.Passed {
			return fail(StatusError, fmt.Sprintf("setup step %d failed (exit %d)", i, res.ExitCode))
		}
	}
	if err := scanForOracleLeaks(corpus, t, wt.Dir); err != nil {
		iso.OracleLeakScan = "leaked"
		return fail(StatusError, "oracle_visible: "+err.Error())
	}
	iso.OracleLeakScan = "clean"

	sampler := startRSSSampler(100*time.Millisecond, filepath.Join(art, "rss.jsonl"))
	samplerDone := false
	finishSampler := func() {
		if !samplerDone {
			samplerDone = true
			s := sampler.finish()
			rec.RSS = &s
		}
	}
	defer finishSampler()

	var stk stackRun
	var peer *MCPServer
	if arm.UsesStack {
		var mem *MemorySpec
		if arm.SeedsMemory {
			mem = t.Memory
		}
		rec.Stack = &StackInfo{Kind: cfg.Stack.Kind, CoreOnly: cfg.Stack.Kind == StackReal && cfg.Stack.coreOnly()}
		stk, err = startStack(ctx, cfg.Stack, stackStart{runDir: filepath.Join(art, "stack"), worktree: wt.Dir, taskID: t.ID, memory: mem, sampler: sampler, self: cfg.self})
		if err != nil {
			rec.Stack.Error = err.Error()
			return fail(StatusError, "start xMustard stack: "+err.Error())
		}
		defer stk.stop()
		rec.Stack.WorkspaceID = stk.workspaceID()
		rec.Stack.Seeds = stk.seeds()
	}
	if arm.Peer != "" {
		p, _ := cfg.peer(arm.Peer)
		peer = &MCPServer{Name: p.Name, Command: p.Command, Args: p.Args, Env: p.Env}
	}
	if err := applyDrift(t.Memory, wt.Dir); err != nil {
		return fail(StatusError, err.Error())
	}
	baseTree, err := wt.SnapshotTree(cfg.workRoot)
	if err != nil {
		return fail(StatusError, "snapshot: "+err.Error())
	}

	configDir := filepath.Join(cfg.workRoot, "clientcfg", runID)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fail(StatusError, err.Error())
	}
	defer os.RemoveAll(configDir)
	req := DriverRequest{Arm: arm, Model: cfg.Model, WorkDir: wt.Dir, Client: cfg.client(), ConfigDir: configDir,
		UserServers: ex.userServers, XmServerName: "xmustard", ExternalSandbox: cfg.contain == ContainSandbox}
	if arm.NeedsHooks {
		req.HookArgs = cfg.Hooks[cfg.Driver]
	}
	ws := ""
	if stk != nil {
		roots, err := stk.agentPhase(ctx)
		if err != nil {
			return fail(StatusError, "xMustard agent phase: "+err.Error())
		}
		sampler.setXmRoots(roots...)
		ws = stk.workspaceID()
		srv := stk.mcpServer()
		if slices.Contains(ex.userServers, srv.Name) {
			// never merge into an operator server of the same name (codex merges -c)
			srv.Name = "xmustard_eval"
		}
		req.XmServerName = srv.Name
		req.Servers = []MCPServer{srv}
		req.PiEnv = stk.piEnv()
	}
	if peer != nil {
		req.Servers = []MCPServer{*peer}
	}
	prompt := buildPrompt(t, arm, ws)
	if err := os.WriteFile(filepath.Join(art, "prompt.txt"), []byte(prompt), 0o644); err != nil {
		return fail(StatusError, err.Error())
	}
	ps := sha256.Sum256([]byte(prompt))
	rec.PromptSHA256 = hex.EncodeToString(ps[:])

	inv, err := ex.driver.Build(req, prompt)
	if err != nil {
		return fail(StatusError, "build client invocation: "+err.Error())
	}
	if cfg.Fake {
		inv, err = ex.fakeInvocation(inv, t, arm, rep)
		if err != nil {
			return fail(StatusError, err.Error())
		}
	}
	if inv, err = contain(cfg.contain, cfg.hidden, []string{filepath.Join(cfg.workRoot, "repos")}, inv); err != nil {
		return fail(StatusError, err.Error())
	}
	sampler.setPhase("agent")
	exit, err := runClient(ctx, ex.driver, inv, wt.Dir, prompt, filepath.Join(art, "transcript.jsonl"), filepath.Join(art, "client.stderr.log"),
		time.Duration(corpus.timeoutSec(t))*time.Second, sampler.setAgentRoot)
	sampler.setPhase("post")
	finishSampler()
	rec.Client = &exit
	if err != nil {
		return fail(StatusError, "client: "+err.Error())
	}
	if tf, err := os.Open(filepath.Join(art, "transcript.jsonl")); err == nil {
		tr := ex.driver.Parse(tf, req.XmServerName)
		tf.Close()
		priceUsage(&tr, cfg.Model, cfg.Pricing)
		rec.Transcript = &tr
	}
	if exit.Canceled {
		return fail(StatusInterrupted, "interrupted during the agent run")
	}

	finalTree, err := wt.SnapshotTree(cfg.workRoot)
	if err != nil {
		return fail(StatusError, "snapshot: "+err.Error())
	}
	churn, err := diffChurn(wt.Dir, baseTree, finalTree, filepath.Join(art, "diff.patch"))
	if err != nil {
		return fail(StatusError, "diff: "+err.Error())
	}
	rec.Churn = &churn
	rec.Localization = editLocalization(t.GoldFiles, churn.Files)

	if stk != nil {
		health, promo := stk.finish(ctx)
		rec.Stack.Health = health
		stk.stop()
		if arm.SeedsMemory && rec.Transcript != nil {
			rec.Memory = memoryMetrics(t.ID, t.Memory, rec.Transcript.XmResults, stk.seeds(), promo)
		}
	}
	if t.Verify != nil {
		v := runCheck(ctx, t.Verify.Cmd, wt.Dir, t.Verify.Env, secondsOr(t.Verify.TimeoutSec, 600), filepath.Join(art, "verify.log"), "verify.log")
		rec.Verify = &v
	}
	if ctx.Err() != nil {
		return fail(StatusInterrupted, "interrupted before the oracle")
	}
	// The agent's group is dead (runClient killed it) and the visible verify step is
	// recorded; only now may the hidden oracle enter the worktree.
	if err := stageOracle(corpus, t, wt.Dir); err != nil {
		return fail(StatusError, "stage oracle: "+err.Error())
	}
	iso.OracleStagedPost = true
	o := runCheck(ctx, t.Oracle.Cmd, wt.Dir, t.Oracle.Env, secondsOr(t.Oracle.TimeoutSec, 600), filepath.Join(art, "oracle.log"), "oracle.log")
	rec.Oracle = &o
	if ctx.Err() != nil {
		return fail(StatusInterrupted, "interrupted during the oracle")
	}
	if o.Error != "" {
		return fail(StatusError, "oracle did not run: "+o.Error)
	}
	rec.Resolved = o.Passed
	rec.Status = StatusCompleted
	return rec
}

// fakeInvocation swaps the client binary for this executable's fake agent and gives
// it the canned solution (a copy outside every hidden path).
func (ex *executor) fakeInvocation(inv Invocation, t *Task, arm Arm, rep int) (Invocation, error) {
	cfg := ex.cfg
	env := append(inv.Env, envAsMain+"=1")
	if t.Reference != nil {
		dir := filepath.Join(cfg.workRoot, "fake", t.ID+"-"+armDir(arm.Name)+"-r"+strconv.Itoa(rep))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return inv, err
		}
		b, err := os.ReadFile(ex.corpus.resolve(t.Reference.Patch))
		if err != nil {
			return inv, err
		}
		patch := filepath.Join(dir, "solution.patch")
		if err := os.WriteFile(patch, b, 0o644); err != nil {
			return inv, err
		}
		env = append(env, envFakePatch+"="+patch)
	}
	if slices.Contains(cfg.FakeFailArms, arm.Name) {
		env = append(env, envFakeSolve+"=0")
	}
	if len(cfg.fakeProbe) > 0 {
		env = append(env, envFakeProbe+"="+strings.Join(cfg.fakeProbe, "|"))
	}
	if cfg.fakeSleepMS > 0 {
		env = append(env, envFakeSleep+"="+strconv.Itoa(cfg.fakeSleepMS))
	}
	if cfg.fakeExit != 0 {
		env = append(env, envFakeExit+"="+strconv.Itoa(cfg.fakeExit))
	}
	args := append([]string{"fake-agent", "--emulate", cfg.Driver, "--"}, inv.Args...)
	return Invocation{Bin: cfg.self, Args: args, Env: env, Stdin: inv.Stdin}, nil
}

// buildPrompt is the task prompt, the visible verify command, and (xMustard arms
// only) the one line naming the workspace the tools need. The line is the only
// prompt difference between arms and is recorded via prompt_sha256.
func buildPrompt(t *Task, arm Arm, ws string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(t.Prompt))
	if t.Verify != nil {
		b.WriteString("\n\nCheck your change with: " + strings.Join(t.Verify.Cmd, " "))
	}
	if arm.UsesStack && ws != "" {
		b.WriteString("\n\nxMustard tools are available for this repository; use workspace_id \"" + ws + "\".")
	}
	b.WriteString("\n")
	return b.String()
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
