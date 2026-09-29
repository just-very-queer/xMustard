package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"xmustard/api-go/internal/redact"
)

// Upstream references (WS-41, PAR-CTX-09). When the client already persisted the
// original (Claude Code writes large tool results to a file), xMustard keeps a hashed
// reference instead of a second copy: the copy would hold bytes twice and, before
// redaction, a secret in a second place. The reference is verified by streaming the
// file once, so the hash is real, and it costs no quota. xMustard never serves
// the referenced bytes: a read of such a handle answers ErrUpstreamRef with the
// reference, and the client reads its own file.

// ErrUpstreamRef answers a read of a handle whose original the client holds.
var ErrUpstreamRef = errors.New("the original is held by the client; read it at the upstream reference")

// ErrUpstreamInvalid refuses an upstream reference that is malformed, outside the
// allowed roots, a secret path, or does not match its hash.
var ErrUpstreamInvalid = errors.New("invalid upstream reference")

// MaxUpstreamBytes bounds one verified file.
const MaxUpstreamBytes = 1 << 30

// UpstreamRef names an original the client persisted.
type UpstreamRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

func invalidUpstream(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUpstreamInvalid, fmt.Sprintf(format, a...))
}

// Verify checks the reference: an absolute regular file (not a symlink) inside one of
// roots, not a secret path, whose streamed SHA-256 and size equal the claim. Guards
// run in order and fail closed; the file is read in fixed 64 KiB chunks.
func (r UpstreamRef) Verify(ctx context.Context, roots []string) error {
	if !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path {
		return invalidUpstream("path must be absolute and clean")
	}
	if len(r.SHA256) != sha256.Size*2 || r.Bytes < 0 || r.Bytes > MaxUpstreamBytes {
		return invalidUpstream("sha256 and bytes are required (at most %d bytes)", MaxUpstreamBytes)
	}
	if pattern, ok := redact.MatchSecretPath(r.Path); ok {
		return fmt.Errorf("%w: %w (%s)", ErrUpstreamInvalid, ErrSecretPath, pattern)
	}
	if info, err := os.Lstat(r.Path); err != nil || !info.Mode().IsRegular() {
		return invalidUpstream("not a regular file")
	}
	if !underAny(r.Path, roots) {
		return invalidUpstream("outside the allowed roots")
	}
	f, err := os.Open(r.Path)
	if err != nil {
		return invalidUpstream("unreadable")
	}
	defer f.Close()
	sum, n, err := hashStream(ctx, f, r.Bytes+1)
	switch {
	case err != nil:
		return err
	case n != r.Bytes || !strings.EqualFold(sum, r.SHA256):
		return invalidUpstream("hash or size does not match the file")
	}
	return nil
}

// underAny reports whether path, with symlinks resolved, lies under a resolved root.
func underAny(path string, roots []string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(root, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// hashStream hashes at most limit bytes of r in 64 KiB chunks, checking ctx between
// chunks, and returns the hex digest and the bytes read.
func hashStream(ctx context.Context, r io.Reader, limit int64) (string, int64, error) {
	h, buf := sha256.New(), make([]byte, 64<<10)
	var n int64
	for n < limit {
		if err := ctx.Err(); err != nil {
			return "", n, err
		}
		m, err := r.Read(buf[:min(int64(len(buf)), limit-n)])
		h.Write(buf[:m])
		n += int64(m)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", n, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
