// xmustard-eval is the paired outcome-evaluation executor (PAR-EVAL-01). It runs a
// task corpus under several arms (baseline, baseline_nomcp, xmustard_mcp,
// xmustard_mcp_hooks, xmustard_memory, peer:<name>) with a real headless client
// (claude -p stream-json, codex exec --json, pi --mode rpc), one fresh detached
// worktree per run, a hidden harness-owned oracle deciding resolution, tokens and
// cost from the clients' final events, diff churn, sampled RSS of the xMustard
// process tree, and coding-memory lifecycle metrics (PAR-EVAL-02). The report reuses
// memory_harness.go's paired statistics. The executor is an operator tool: it runs
// outside the measured xMustard process tree.
//
//	xmustard-eval run      --corpus eval/tasks/seed.yaml --out DIR [--config run.yaml] [flags]
//	xmustard-eval validate --corpus eval/tasks/seed.yaml [--oracles] [--out DIR]
//	xmustard-eval report   --out DIR
//
// A dry run needs no credentials: --driver fake:claude (or fake:codex, fake:pi).
// See eval/tasks/README.md for the corpus schema and the real-model operator step.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func realMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "validate":
		return cmdValidate(args[1:], stdout, stderr)
	case "report":
		return cmdReport(args[1:], stdout, stderr)
	case "fake-agent":
		return runFakeAgent(args[1:], stdin, stdout)
	case "watchdog":
		return runWatchdog(args[1:], stdin)
	case "stub-mcp":
		if err := runStubMCP(stdin, stdout); err != nil {
			fmt.Fprintln(stderr, "stub-mcp:", err)
			return 1
		}
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage:
  xmustard-eval run --corpus FILE --out DIR [--config FILE] [--driver claude|codex|pi|fake:<driver>]
                    [--model M] [--arms a,b] [--repeats N] [--seed S] [--tasks id,id]
                    [--stack real|stub|none] [--api-bin P] [--mcp-bin P] [--core-bin P]
                    [--containment auto|sandbox-exec|bwrap|none] [--fake-fail-arms a,b] [--keep-worktrees]
  xmustard-eval validate --corpus FILE [--oracles] [--tasks id,id] [--out DIR]
                         [--containment auto|sandbox-exec|bwrap|none]
  xmustard-eval report --out DIR
`)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	corpusPath := fs.String("corpus", "", "task corpus YAML")
	outDir := fs.String("out", "", "output directory (must not already hold runs.jsonl)")
	configPath := fs.String("config", "", "run config YAML")
	driver := fs.String("driver", "", "claude | codex | pi | fake:<driver>")
	model := fs.String("model", "", "fixed model for every run")
	arms := fs.String("arms", "", "comma-separated arms")
	repeats := fs.Int("repeats", 0, "repetitions per task and arm")
	seed := fs.Int64("seed", 0, "seed for arm order and bootstrap")
	tasks := fs.String("tasks", "", "comma-separated task ids")
	stack := fs.String("stack", "", "real | stub | none")
	apiBin := fs.String("api-bin", "", "xmustard-api binary")
	mcpBin := fs.String("mcp-bin", "", "xmustard-mcp binary")
	coreBin := fs.String("core-bin", "", "xmustard-core binary (default $XMUSTARD_CORE_BIN)")
	containment := fs.String("containment", "", "auto | sandbox-exec | bwrap | none")
	fakeFail := fs.String("fake-fail-arms", "", "fake driver: arms whose runs leave the task unsolved")
	keep := fs.Bool("keep-worktrees", false, "keep worktrees for debugging")
	fakeSleep := fs.Int("fake-sleep-ms", 0, "fake driver: milliseconds the fake agent works before finishing (dry runs only)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *corpusPath == "" || *outDir == "" {
		fmt.Fprintln(stderr, "run: --corpus and --out are required")
		return 2
	}
	corpus, err := LoadCorpus(*corpusPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg := &RunConfig{}
	if *configPath != "" {
		if err := loadYAMLStrict(*configPath, cfg); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["driver"] {
		cfg.Driver = *driver
	}
	if set["model"] {
		cfg.Model = *model
	}
	if set["arms"] {
		cfg.Arms = splitList(*arms)
	}
	if set["repeats"] {
		cfg.Repeats = *repeats
	}
	if set["seed"] {
		cfg.Seed = *seed
	}
	if set["tasks"] {
		cfg.Tasks = splitList(*tasks)
	}
	if set["stack"] {
		cfg.Stack.Kind = *stack
	}
	if set["api-bin"] {
		cfg.Stack.APIBin = *apiBin
	}
	if set["mcp-bin"] {
		cfg.Stack.MCPBin = *mcpBin
	}
	if set["core-bin"] {
		cfg.Stack.CoreBin = *coreBin
	}
	if set["containment"] {
		cfg.Containment = *containment
	}
	if set["fake-fail-arms"] {
		cfg.FakeFailArms = splitList(*fakeFail)
	}
	if set["keep-worktrees"] {
		cfg.KeepWorktrees = *keep
	}
	cfg.fakeSleepMS = *fakeSleep
	if err := cfg.prepare(corpus, *outDir); err != nil {
		fmt.Fprintln(stderr, "run config:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := Execute(ctx, cfg, corpus)
	if rep != nil {
		fmt.Fprintf(stdout, "report: %s\n", filepath.Join(cfg.outDir, "report.md"))
		fmt.Fprintf(stdout, "runs: %s\n", countsLine(rep.Counts))
	}
	if cfg.KeepWorktrees && cfg.workRoot != "" {
		fmt.Fprintf(stdout, "worktrees kept in: %s (remove it when done)\n", cfg.workRoot)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			if cfg.KeepWorktrees {
				fmt.Fprintf(stderr, "interrupted; worktrees kept in %s, partial results written\n", cfg.workRoot)
			} else {
				fmt.Fprintln(stderr, "interrupted; worktrees removed, partial results written")
			}
			return 130
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func cmdReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outDir := fs.String("out", "", "output directory of a run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *outDir == "" {
		fmt.Fprintln(stderr, "report: --out is required")
		return 2
	}
	rep, err := writeReport(*outDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "report: %s\nruns: %s\n", filepath.Join(*outDir, "report.md"), countsLine(rep.Counts))
	return 0
}

func loadYAMLStrict(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// OracleValidation is one task's `validate --oracles` result.
type OracleValidation struct {
	TaskID          string   `json:"task_id"`
	OracleFailsBase bool     `json:"oracle_fails_on_baseline"`
	OraclePassesRef bool     `json:"oracle_passes_on_reference"`
	VerifyPassesRef *bool    `json:"visible_verify_passes_on_reference,omitempty"`
	Valid           bool     `json:"valid"`
	Error           string   `json:"error,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	// log paths, with --out only
	BaselineOracleLog string `json:"baseline_oracle_log,omitempty"`
	RefOracleLog      string `json:"reference_oracle_log,omitempty"`
}

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	corpusPath := fs.String("corpus", "", "task corpus YAML")
	oracles := fs.Bool("oracles", false, "also check every oracle fails on the untouched task and passes on its reference patch")
	tasks := fs.String("tasks", "", "comma-separated task ids")
	out := fs.String("out", "", "keep the setup, verify and oracle logs in this directory (they hold hidden oracle output; without it they are deleted)")
	containment := fs.String("containment", "", "auto | sandbox-exec | bwrap | none, as for run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *corpusPath == "" {
		fmt.Fprintln(stderr, "validate: --corpus is required")
		return 2
	}
	corpus, err := LoadCorpus(*corpusPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "schema ok: %s (%d tasks)\n", corpus.Name, len(corpus.Tasks))
	if !*oracles {
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	results, err := validateOracles(ctx, corpus, splitList(*tasks), *containment, *out)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(results)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, r := range results {
		if !r.Valid {
			if *out == "" {
				fmt.Fprintln(stderr, "logs were not kept; pass --out DIR to keep them")
			}
			return 1
		}
	}
	return 0
}

// validateOracles checks WS-63's corpus rule: each oracle fails on the untouched
// starting state and passes once the reference patch is applied. Every command runs
// as in a run: setup and verify under a run's profile, the oracle under the judge
// profile, in a copy reached through a link at the worktree's own path. Logs hold
// hidden oracle output, so they are kept only in logDir, when given.
func validateOracles(ctx context.Context, corpus *Corpus, only []string, containment, logDir string) ([]OracleValidation, error) {
	mode, err := resolveContainment(containment)
	if err != nil {
		return nil, err
	}
	hidden := corpus.hiddenPaths()
	hidden = append(hidden, repoPaths(hidden)...)
	if logDir != "" {
		if logDir, err = filepath.Abs(logDir); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return nil, err
		}
		hidden = append(hidden, logDir)
	}
	work, err := os.MkdirTemp("", "xmustard-eval-validate-")
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(work); err == nil {
		work = real
	}
	reg := newWorktreeRegistry()
	defer func() {
		_ = reg.removeAll()
		_ = os.RemoveAll(work)
	}()
	if err := makeRunAreas(work); err != nil {
		return nil, err
	}
	logRoot := logDir
	if logRoot == "" {
		logRoot = filepath.Join(work, "harness", "logs")
	}
	ex := &executor{cfg: &RunConfig{contain: mode, workRoot: work, hidden: hidden}, corpus: corpus}
	var out []OracleValidation
	for i := range corpus.Tasks {
		t := &corpus.Tasks[i]
		if len(only) > 0 && !slices.Contains(only, t.ID) {
			continue
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		out = append(out, ex.validateOneOracle(ctx, t, logRoot, logDir != "", reg))
	}
	return out, nil
}

func (ex *executor) validateOneOracle(ctx context.Context, t *Task, logRoot string, keepLogs bool, reg *worktreeRegistry) (v OracleValidation) {
	corpus, work := ex.corpus, ex.cfg.workRoot
	v.TaskID = t.ID
	fail := func(err error) OracleValidation {
		v.Error = err.Error()
		return v
	}
	if t.Reference == nil {
		return fail(errors.New("no reference patch: an oracle cannot be validated without one"))
	}
	repo, sha, err := prepareRepo(corpus, t, filepath.Join(work, "repos"))
	if err != nil {
		return fail(err)
	}
	wt, err := newRunWorktree(repo, sha, filepath.Join(work, "runrepos", t.ID), filepath.Join(work, "wt", t.ID),
		repoDirName(corpus, t), filepath.Join(work, "harness", t.ID))
	if err != nil {
		return fail(err)
	}
	reg.add(wt)
	defer func() { _ = reg.remove(wt) }()
	logs := filepath.Join(logRoot, t.ID)
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return fail(err)
	}
	cmd := func(spec CommandSpec, name string, sb *sandbox) check {
		c := check{argv: spec.Cmd, dir: wt.Dir, env: spec.Env, sb: sb, timeout: secondsOr(spec.TimeoutSec, 600), logPath: filepath.Join(logs, name)}
		if keepLogs {
			c.logRel = c.logPath
		}
		return c
	}
	sb := ex.sandboxFor(t.ID)
	for i, s := range t.Setup {
		if r := cmd(s, fmt.Sprintf("setup-%d.log", i), sb).run(ctx); !r.Passed {
			return fail(fmt.Errorf("setup step %d failed (exit %d)", i, r.ExitCode))
		}
	}
	if err := applyDrift(t.Memory, wt.Dir); err != nil {
		return fail(err)
	}
	if err := scanForOracleLeaks(corpus, t, wt.Dir); err != nil {
		return fail(err)
	}
	runOracle := func(which string) (CheckResult, error) {
		judgeRun := filepath.Join(work, "judge", t.ID)
		site := newJudgeSite(wt, judgeRun)
		defer func() {
			_ = site.restore()
			_ = os.RemoveAll(judgeRun)
		}()
		var held *Worktree
		var tree string
		for _, step := range []func() error{
			func() (err error) { held, err = site.hold(wt); return err },
			func() (err error) { tree, err = held.SnapshotTree(); return err },
			func() error { return site.prepare(held, tree) },
			func() error { return stageOracle(corpus, t, site.judgedWorktree()) },
			site.link,
		} {
			if err := step(); err != nil {
				return CheckResult{}, err
			}
		}
		r := cmd(CommandSpec{Cmd: t.Oracle.Cmd, Env: t.Oracle.Env, TimeoutSec: t.Oracle.TimeoutSec}, "oracle-"+which+".log", ex.judgeSandbox(site)).run(ctx)
		if r.Error != "" {
			return r, errors.New("oracle did not run: " + r.Error)
		}
		if w := unrunnableOracle(r.ExitCode); w != "" {
			v.Warnings = append(v.Warnings, which+": "+w)
		}
		return r, nil
	}
	base, err := runOracle("baseline")
	if err != nil {
		return fail(err)
	}
	v.OracleFailsBase, v.BaselineOracleLog = !base.Passed, base.Log
	patch, err := corpus.fileBytes(t.Reference.Patch)
	if err == nil {
		err = applyPatch(wt.Dir, patch)
	}
	if err != nil {
		return fail(err)
	}
	if t.Verify != nil {
		r := cmd(*t.Verify, "verify-reference.log", sb).run(ctx)
		v.VerifyPassesRef = &r.Passed
	}
	ref, err := runOracle("reference")
	if err != nil {
		return fail(err)
	}
	v.OraclePassesRef, v.RefOracleLog = ref.Passed, ref.Log
	v.Valid = v.OracleFailsBase && v.OraclePassesRef && (v.VerifyPassesRef == nil || *v.VerifyPassesRef)
	return v
}
