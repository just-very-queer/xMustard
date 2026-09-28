// Package gitdiff runs Git under the hardened diff policy (WS-64, folded into WS-35;
// PAR-REV-01) for the Go side: merge attestations digest the review diff it produces.
//
// The Rust core owns the policy (rust-core/src/gitdiff.rs), which also parses the -U0
// symbol diff impact reads; this package is its copy of the parts Go runs: the
// environment scrub, the isolation variables, the -c pins, the review mode and the scope
// vocabulary impact's scope= form is checked against. Both are tested against
// rust-core/src/testdata/gitdiff_policy.golden, so the two cannot drift.
//
// Every Git child here ignores the system, global and XDG configuration and the system
// attributes, has every GIT_* and XMUSTARD_* variable removed from its environment (an
// approver's token never reaches it), and runs with hooks, the fsmonitor and the
// attributes file pinned off. Every diff option repository configuration could change
// is on the command line, and revisions follow --end-of-options.
package gitdiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode"
)

// ScrubbedEnvPrefixes are the prefixes of the variables no Git child inherits.
var ScrubbedEnvPrefixes = []string{"GIT_", "XMUSTARD_"}

// IsolationEnv is set on every Git child.
var IsolationEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_GLOBAL=" + os.DevNull,
	"GIT_ATTR_NOSYSTEM=1",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_OPTIONAL_LOCKS=0",
	"LC_ALL=C",
}

// ConfigPins are the -c pins of every Git call.
var ConfigPins = []string{
	"core.hooksPath=" + os.DevNull,
	"core.fsmonitor=false",
	"core.attributesFile=" + os.DevNull,
	"core.bigFileThreshold=512m",
	"diff.suppressBlankEmpty=false",
}

// ReviewConfig are the review mode's own pins: Git's default path quoting, which the
// attestation digests were first taken with.
var ReviewConfig = []string{"core.quotePath=true"}

// ReviewFlags are the options of the review diff an attestation digests: every option
// repository configuration could change (algorithm, context, prefixes, renames, relative
// paths, submodule format, order file, binary and full index), with no external diff,
// no text conversion and no color.
var ReviewFlags = []string{
	"--no-color", "--no-ext-diff", "--no-textconv", "--binary", "--full-index", "--no-renames",
	"--no-relative", "--diff-algorithm=myers", "--indent-heuristic", "--inter-hunk-context=0", "-U3",
	"--src-prefix=a/", "--dst-prefix=b/", "--ignore-submodules=none", "--submodule=short", "-O" + os.DevNull,
}

// Need says whether a scope requires, allows or refuses a revision.
type Need string

const (
	Refused  Need = "refused"
	Optional Need = "optional"
	Required Need = "required"
)

// Scope is one name of the shared scope vocabulary and the revisions it takes.
type Scope struct {
	Name       string
	Base, Head Need
}

// Scopes is rust-core's scope table (SCOPE_NAMES with Scope::needs), in its order;
// workspace is the review vocabulary's name for all.
var Scopes = []Scope{
	{"unstaged", Refused, Refused},
	{"staged", Refused, Refused},
	{"all", Refused, Refused},
	{"workspace", Refused, Refused},
	{"compare", Required, Optional},
	{"commit", Refused, Optional},
	{"range", Required, Optional},
}

// ErrBadScope is returned for a scope name outside Scopes, or a base or head the scope
// does not take or needs.
var ErrBadScope = errors.New("bad diff scope")

// CheckScope refuses an unknown scope, a revision the scope does not take and a missing
// one it needs, before any Git runs.
func CheckScope(scope, base, head string) error {
	i := slices.IndexFunc(Scopes, func(s Scope) bool { return s.Name == scope })
	if i < 0 {
		return fmt.Errorf("unknown scope %q: %w", scope, ErrBadScope)
	}
	for _, r := range []struct {
		name  string
		need  Need
		given bool
	}{{"base", Scopes[i].Base, base != ""}, {"head", Scopes[i].Head, head != ""}} {
		switch {
		case r.need == Required && !r.given:
			return fmt.Errorf("scope %s needs %s: %w", scope, r.name, ErrBadScope)
		case r.need == Refused && r.given:
			return fmt.Errorf("scope %s takes no %s: %w", scope, r.name, ErrBadScope)
		}
	}
	return nil
}

// ErrBadRef is returned for a revision refused before Git sees it or one that names no
// commit.
var ErrBadRef = errors.New("not a revision")

// MaxRefLen bounds a revision expression.
const MaxRefLen = 256

// ReviewDiffArgs is the review diff from one commit to another.
func ReviewDiffArgs(from, to string) []string {
	args := append([]string{"diff"}, ReviewFlags...)
	return append(args, "--end-of-options", from, to, "--")
}

// Env is the environment of a Git child: this process's, less the scrubbed variables,
// plus the isolation variables.
func Env() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !scrubbed(kv) {
			out = append(out, kv)
		}
	}
	return append(out, IsolationEnv...)
}

func scrubbed(kv string) bool {
	for _, p := range ScrubbedEnvPrefixes {
		if strings.HasPrefix(kv, p) {
			return true
		}
	}
	return false
}

// Command is a Git child in dir under the policy, with the review mode's pins.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := make([]string, 0, 2*(len(ConfigPins)+len(ReviewConfig))+len(args))
	for _, c := range append(append([]string{}, ConfigPins...), ReviewConfig...) {
		full = append(full, "-c", c)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, Env(), time.Second
	return cmd
}

// Run runs Git in dir within timeout, writing stdout to w; a failure carries the first
// 4 KiB of stderr.
func Run(ctx context.Context, dir string, w io.Writer, timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := Command(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &limitedBuffer{buf: &stderr, max: 4 << 10}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// CheckRef refuses a revision shaped like an option, or one that could smuggle a second
// argument, before Git sees it. The checks run in order and the first failure answers.
func CheckRef(ref string) error {
	switch {
	case ref == "":
		return fmt.Errorf("an empty revision: %w", ErrBadRef)
	case len(ref) > MaxRefLen:
		return fmt.Errorf("a revision longer than %d bytes: %w", MaxRefLen, ErrBadRef)
	case strings.HasPrefix(ref, "-"):
		return fmt.Errorf("revision %q starts with '-': %w", ref, ErrBadRef)
	case strings.IndexFunc(ref, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return fmt.Errorf("revision %q contains whitespace or a control character: %w", ref, ErrBadRef)
	}
	return nil
}

// ResolveCommit resolves ref to a full commit id in dir.
func ResolveCommit(ctx context.Context, dir, ref string, timeout time.Duration) (string, error) {
	ref = strings.TrimSpace(ref)
	if err := CheckRef(ref); err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := Run(ctx, dir, &out, timeout, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}"); err != nil {
		return "", fmt.Errorf("ref %q does not name a commit: %w", ref, ErrBadRef)
	}
	return strings.TrimSpace(out.String()), nil
}

// RenderPolicy lists the policy as the golden spec does, for the lines this package
// implements.
func RenderPolicy() string {
	var b strings.Builder
	null := func(s string) string { return strings.ReplaceAll(s, os.DevNull, "/dev/null") }
	for _, p := range ScrubbedEnvPrefixes {
		fmt.Fprintf(&b, "scrub\t%s\n", p)
	}
	for _, kv := range IsolationEnv {
		fmt.Fprintf(&b, "env\t%s\n", null(kv))
	}
	for _, c := range ConfigPins {
		fmt.Fprintf(&b, "config\t%s\n", null(c))
	}
	for _, c := range ReviewConfig {
		fmt.Fprintf(&b, "review.config\t%s\n", c)
	}
	for _, f := range ReviewFlags {
		fmt.Fprintf(&b, "review\t%s\n", null(f))
	}
	for _, sc := range Scopes {
		fmt.Fprintf(&b, "scope\t%s\tbase=%s\thead=%s\n", sc.Name, sc.Base, sc.Head)
	}
	return b.String()
}

// limitedBuffer keeps the first max bytes written and discards the rest.
type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}
