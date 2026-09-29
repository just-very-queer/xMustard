package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func captureClass(t *testing.T, s *Store, ws string, class RetentionClass, raw []byte, up *UpstreamRef, roots []string) (*Delivery, error) {
	t.Helper()
	sp, err := s.NewSpool(ws)
	if err != nil {
		t.Fatal(err)
	}
	sp.SetClass(class)
	if _, err := sp.Write(raw); err != nil {
		t.Fatal(err)
	}
	return s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: ws, RepoScope: "/repo", Issuer: "test",
		Tool: "Bash", Status: 200, ContentType: "text/plain", Retain: true, Class: class, Upstream: up, UpstreamRoots: roots})
}

func TestRetentionClassesExpireIndependently(t *testing.T) {
	s, now := testStore(t, func(l *Limits) { l.Retention = time.Hour })
	raw := bytes.Repeat([]byte("x"), 20<<10)
	hook, err := captureClass(t, s, "ws", ClassHook, raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := captureClass(t, s, "ws", ClassSnapshot, raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hook.RetentionClass != ClassHook || snap.RetentionClass != ClassSnapshot {
		t.Fatalf("classes = %q %q", hook.RetentionClass, snap.RetentionClass)
	}
	*now = now.Add(2 * time.Hour) // past the hook TTL (1h), inside the snapshot TTL (3h)
	read := func(h *Delivery) error {
		_, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: h.Handle})
		return err
	}
	if err := read(hook); !errors.Is(err, ErrExpired) {
		t.Fatalf("hook after 2h = %v, want expired", err)
	}
	if err := read(snap); err != nil {
		t.Fatalf("snapshot after 2h = %v, want readable", err)
	}
}

func TestClassQuotaFullNeverEvictsUnexpired(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.WorkspaceQuota = 200 << 10 }) // hook 150 KiB, snapshot 50 KiB
	raw := bytes.Repeat([]byte("y"), 60<<10)
	first, err := captureClass(t, s, "ws", ClassHook, raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captureClass(t, s, "ws", ClassHook, raw, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := captureClass(t, s, "ws", ClassHook, raw, nil, nil); !errors.Is(err, ErrQuotaFull) {
		t.Fatalf("third hook capture = %v, want quota full", err)
	}
	// the hook class is full but the total is not: own results keep their room
	if _, err := captureClass(t, s, "ws", ClassOwn, bytes.Repeat([]byte("z"), 40<<10), nil, nil); err != nil {
		t.Fatalf("own capture = %v", err)
	}
	if _, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: first.Handle}); err != nil {
		t.Fatalf("an unexpired original was lost: %v", err)
	}
}

func upstreamFile(t *testing.T, dir string, data []byte) UpstreamRef {
	t.Helper()
	p := filepath.Join(dir, "tool-result.txt")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return UpstreamRef{Path: p, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}
}

func TestUpstreamReferenceKeepsNoCopyAndNoQuota(t *testing.T) {
	s, _ := testStore(t, nil)
	dir := t.TempDir()
	data := bytes.Repeat([]byte("orig\n"), 30<<10)
	ref := upstreamFile(t, dir, data)
	d, err := captureClass(t, s, "ws", ClassHook, data, &ref, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if d.Upstream == nil || d.Upstream.SHA256 != ref.SHA256 {
		t.Fatalf("delivery upstream = %+v", d.Upstream)
	}
	if used, _ := s.Retained("ws"); used != 0 {
		t.Fatalf("an upstream reference charged %d bytes", used)
	}
	if _, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle}); !errors.Is(err, ErrUpstreamRef) {
		t.Fatalf("read = %v, want the upstream reference answer", err)
	}
	key, _ := handleKey(d.Handle)
	if _, err := os.Stat(filepath.Join(s.root, "ws", key, "raw.bin")); !os.IsNotExist(err) {
		t.Fatalf("raw.bin was kept: %v", err)
	}
}

func TestUpstreamReferenceIsRefusedWhenUnsound(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	data := []byte("the original")
	good := upstreamFile(t, dir, data)
	bad := good
	bad.SHA256 = hex.EncodeToString(make([]byte, 32))
	wrongSize := good
	wrongSize.Bytes++
	relative := good
	relative.Path = "tool-result.txt"
	secretDir := filepath.Join(dir, ".ssh")
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := upstreamFile(t, secretDir, data)
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(good.Path, link); err != nil {
		t.Fatal(err)
	}
	linked := good
	linked.Path = link
	for name, c := range map[string]struct {
		ref   UpstreamRef
		roots []string
	}{
		"hash mismatch": {bad, []string{dir}}, "size mismatch": {wrongSize, []string{dir}}, "relative": {relative, []string{dir}},
		"outside roots": {good, []string{other}}, "secret path": {secret, []string{dir}}, "symlink": {linked, []string{dir}},
	} {
		if err := c.ref.Verify(context.Background(), c.roots); !errors.Is(err, ErrUpstreamInvalid) {
			t.Errorf("%s: Verify = %v, want ErrUpstreamInvalid", name, err)
		}
	}
	if err := good.Verify(context.Background(), []string{dir}); err != nil {
		t.Fatalf("a sound reference: %v", err)
	}
}

type countingReader struct {
	left, maxRead int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.left == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), c.left)
	c.maxRead = max(c.maxRead, n)
	c.left -= n
	return int(n), nil
}

func TestUpstreamHashStreamsInFixedChunks(t *testing.T) {
	const size = 96 << 20
	r := &countingReader{left: size}
	_, n, err := hashStream(context.Background(), r, size)
	if err != nil || n != size {
		t.Fatalf("hashStream = %d, %v", n, err)
	}
	if r.maxRead > 64<<10 {
		t.Fatalf("hashStream asked for %d bytes at once", r.maxRead)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := hashStream(ctx, &countingReader{left: size}, size); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled hashStream = %v", err)
	}
}

// A reference must describe the uploaded bytes: a valid file that is not the output
// is refused, and a hook JSON body keeps a copy instead of a reference.
func TestUpstreamReferenceIsBoundToTheUploadedBody(t *testing.T) {
	s, _ := testStore(t, nil)
	dir := t.TempDir()
	data := bytes.Repeat([]byte("orig\n"), 30<<10)
	ref := upstreamFile(t, dir, data)
	observe := func(body []byte) error {
		_, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", Format: FormatRaw,
			Body: bytes.NewReader(body), Meta: CaptureMeta{Tool: "Bash"}, Upstream: &ref, UpstreamRoots: []string{dir}})
		return err
	}
	if err := observe(append(bytes.Clone(data), "more\n"...)); !errors.Is(err, ErrUpstreamInvalid) {
		t.Fatalf("a body other than the referenced file: %v, want ErrUpstreamInvalid", err)
	}
	if err := observe(data); err != nil {
		t.Fatalf("the matching body: %v", err)
	}
}

// An upstream capture is charged to the workspace only: a full hook class does not
// refuse it, and a class refusal names the class.
func TestUpstreamCaptureSkipsClassQuota(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.WorkspaceQuota = 200 << 10 })
	dir := t.TempDir()
	if _, err := captureClass(t, s, "ws", ClassHook, bytes.Repeat([]byte("h"), 120<<10), nil, nil); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("orig\n"), 12<<10)
	ref := upstreamFile(t, dir, data)
	sp, err := s.NewSpool("ws")
	if err != nil {
		t.Fatal(err)
	}
	sp.SetClass(ClassHook)
	sp.upstream = true
	sp.Write(data)
	if sp.QuotaFull() {
		t.Fatalf("an upstream spool was refused by the class quota: %s", sp.refused)
	}
	if _, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", RepoScope: "/repo", Issuer: "test",
		Tool: "Bash", Status: 200, ContentType: "text/plain", Retain: true, Class: ClassHook, Upstream: &ref, UpstreamRoots: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	_, err = captureClass(t, s, "ws", ClassHook, bytes.Repeat([]byte("h"), 60<<10), nil, nil)
	if !errors.Is(err, ErrQuotaFull) || !strings.Contains(err.Error(), "hook class quota") {
		t.Fatalf("a full hook class: %v, want ErrQuotaFull naming the class", err)
	}
}
