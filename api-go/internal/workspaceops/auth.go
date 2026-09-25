package workspaceops

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// tokenStoreMu serializes the load-modify-write on agent_tokens.json. Without it,
// concurrent mint/rotate/revoke calls race (last-writer-wins drops the other's
// change — a lost revoke would silently resurrect a token while reporting success).
var tokenStoreMu sync.Mutex

// maxTTLSeconds bounds a token's lifetime well under the int64-nanosecond overflow
// of time.Duration (~292 years), so an attacker-supplied huge ttl can't wrap to a
// negative/already-expired instant.
const maxTTLSeconds = 100 * 365 * 24 * 3600 // ~100 years

// Bearer-token auth for self-hosted deployments. Each principal gets its own token
// mapped to an identity; the verify path authenticates via that token rather than a
// caller-asserted string, so one token cannot stand in for multiple distinct
// identities. Tokens are stored hashed (sha256) at rest; the raw token is returned
// once at mint time and is not recoverable.

type Principal struct {
	ID string `json:"id"`
	// Role is the token's role spec as minted: one role or several joined with "+"
	// (see auth_roles.go). Legacy specs are admin, agent and readonly.
	Role string `json:"role"`
	// Roles is the expanded set Role holds (every role implies reader; admin holds
	// all). Filled when a token resolves.
	Roles []string `json:"roles,omitempty"`
	// Workspaces, when non-empty, restricts this principal to those workspace ids
	// (per-worker token scoping). Empty/nil means unrestricted — backward-compatible
	// with existing unscoped tokens and operator/env tokens.
	Workspaces []string `json:"workspaces,omitempty"`
}

// AllowsWorkspace reports whether the principal may act on workspaceID. An empty
// scope claim is unrestricted.
func (p *Principal) AllowsWorkspace(workspaceID string) bool {
	if p == nil || len(p.Workspaces) == 0 {
		return true
	}
	for _, w := range p.Workspaces {
		if w == workspaceID {
			return true
		}
	}
	return false
}

type tokenRecord struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	TokenSHA256 string `json:"token_sha256"`
	CreatedAt   string `json:"created_at"`
	// ExpiresAt is an RFC3339 instant after which the token is rejected; empty
	// means it never expires. Enforced in ResolveToken.
	ExpiresAt string `json:"expires_at,omitempty"`
	// Workspaces restricts the token to these workspace ids (empty = all).
	Workspaces []string `json:"workspaces,omitempty"`
}

// tokenExpired reports whether an RFC3339 ExpiresAt is set and in the past. A
// malformed timestamp is treated as expired (fail closed).
func tokenExpired(expiresAt string) bool {
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		// also accept the RFC3339Nano that nowUTC emits
		if t, err = time.Parse(time.RFC3339Nano, expiresAt); err != nil {
			return true // unparseable expiry → reject, don't fail open
		}
	}
	return time.Now().UTC().After(t)
}

func tokensPath(dataDir string) string { return filepath.Join(dataDir, "agent_tokens.json") }

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func loadTokenRecords(dataDir string) ([]tokenRecord, error) {
	var recs []tokenRecord
	if err := readJSON(tokensPath(dataDir), &recs); err != nil {
		if os.IsNotExist(err) {
			return []tokenRecord{}, nil
		}
		return nil, err
	}
	return recs, nil
}

// credential is one resolvable bearer credential: the SHA-256 of the raw token and
// the principal it stands for.
type credential struct {
	digest    [sha256.Size]byte
	principal Principal
	expiresAt string
}

func newPrincipal(id, roleSpec string, workspaces []string) Principal {
	return Principal{ID: id, Role: roleSpec, Roles: ExpandRoles(roleSpec), Workspaces: workspaces}
}

// Token store cache (PAR-SEC-07). The auth middleware resolves a token on every
// request; the store is parsed once and reused while the file keeps the same
// identity (inode), modification time and size. A file whose mtime is within
// tokenCacheRacyWindow of the load is re-read next time, so a same-size rewrite
// inside one timestamp tick is still seen. Mint, rotate and revoke drop the entry
// explicitly after their write.

const tokenCacheRacyWindow = 2 * time.Second

type tokenFileCache struct {
	info     os.FileInfo // nil: the file was absent
	loadedAt time.Time
	recs     []tokenRecord
	creds    []credential
	err      error
}

var (
	tokenCacheMu sync.Mutex
	tokenCache   = map[string]*tokenFileCache{}
	// tokenStoreParses counts reads of the token file from disk (tests).
	tokenStoreParses atomic.Int64
)

// cachedTokenStore returns the parsed token file, re-reading it only when the file
// changed. An unreadable or corrupt store returns its error (callers fail closed).
func cachedTokenStore(dataDir string) (*tokenFileCache, error) {
	path := tokensPath(dataDir)
	info, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	tokenCacheMu.Lock()
	defer tokenCacheMu.Unlock()
	c := tokenCache[path]
	if statErr != nil { // absent file: no file-backed tokens
		if c == nil || c.info != nil {
			c = &tokenFileCache{loadedAt: time.Now()}
			tokenCache[path] = c
		}
		return c, nil
	}
	if c != nil && c.info != nil && os.SameFile(c.info, info) && c.info.ModTime().Equal(info.ModTime()) &&
		c.info.Size() == info.Size() && c.loadedAt.Sub(info.ModTime()) > tokenCacheRacyWindow {
		return c, c.err
	}
	tokenStoreParses.Add(1)
	recs, err := loadTokenRecords(dataDir)
	c = &tokenFileCache{info: info, loadedAt: time.Now(), recs: recs, err: err}
	for _, r := range recs {
		raw, derr := hex.DecodeString(r.TokenSHA256)
		if derr != nil || len(raw) != sha256.Size {
			continue // a malformed digest can never match
		}
		cred := credential{principal: newPrincipal(r.ID, fallbackString(r.Role, roleAgent), r.Workspaces), expiresAt: r.ExpiresAt}
		copy(cred.digest[:], raw)
		c.creds = append(c.creds, cred)
	}
	tokenCache[path] = c
	return c, err
}

// invalidateTokenCache drops the cached store after a mint, rotate or revoke.
func invalidateTokenCache(dataDir string) {
	tokenCacheMu.Lock()
	delete(tokenCache, tokensPath(dataDir))
	tokenCacheMu.Unlock()
}

// envCredentials caches the parse of XMUSTARD_AUTH_TOKENS by its raw value.
var envCredentials struct {
	sync.Mutex
	raw   string
	creds []credential
}

// envTokenCredentials parses XMUSTARD_AUTH_TOKENS="id:role:rawtoken,..." (the raw
// tokens are hashed in memory and never persisted). role may be a "+"-joined spec.
func envTokenCredentials() []credential {
	raw := strings.TrimSpace(os.Getenv("XMUSTARD_AUTH_TOKENS"))
	envCredentials.Lock()
	defer envCredentials.Unlock()
	if raw == envCredentials.raw {
		return envCredentials.creds
	}
	var creds []credential
	for _, item := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(item), ":", 3)
		if len(parts) != 3 {
			continue
		}
		id, role, tok := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), parts[2]
		// Require a minimum token length to resist brute-force guessing.
		if id == "" || len(strings.TrimSpace(tok)) < 24 {
			continue
		}
		creds = append(creds, credential{digest: sha256.Sum256([]byte(tok)), principal: newPrincipal(id, normalizeRole(role), nil)})
	}
	envCredentials.raw, envCredentials.creds = raw, creds
	return creds
}

// normalizeRole maps an env role spec to a valid one. A blank role is the historical
// "agent"; an unknown one becomes the least-privileged "readonly", so a typo in
// XMUSTARD_AUTH_TOKENS can't mint an unconstrained principal.
func normalizeRole(role string) string {
	spec, err := ParseRoleSpec(role)
	if err != nil {
		return roleReadonly
	}
	return spec
}

// HasAuthConfigured reports whether any tokens exist (file or env) — i.e. whether
// auth should be enforced in "auto" mode. Fails CLOSED: if the token store exists
// but can't be read/parsed (corruption, bad perms), it returns true so a damaged
// file can't silently disable auth and open every admin gate. A genuinely-absent
// file (os.IsNotExist) correctly reports false.
func HasAuthConfigured(dataDir string) bool {
	if len(envTokenCredentials()) > 0 {
		return true
	}
	store, err := cachedTokenStore(dataDir)
	if err != nil {
		return true // unreadable token store → assume auth is configured (fail closed)
	}
	return len(store.recs) > 0
}

// ResolveToken returns the Principal for a raw bearer token, or nil if unknown.
func ResolveToken(dataDir, raw string) *Principal {
	p, _ := ResolveAuth(dataDir, raw)
	return p
}

// digestCompare is the comparison matchCredential applies to every credential. It
// is a variable only so a test can count the comparisons.
var digestCompare = subtle.ConstantTimeCompare

// matchCredential compares digest against every credential in constant time per
// comparison and without stopping at a match, and returns the first match.
func matchCredential(creds []credential, digest [sha256.Size]byte) *credential {
	var found *credential
	for i := range creds {
		if digestCompare(creds[i].digest[:], digest[:]) == 1 && found == nil {
			found = &creds[i]
		}
	}
	return found
}

// ResolveAuth resolves a bearer token AND reports whether auth is configured, from
// one (cached) view of the token store — the auth middleware needs both on every
// request. An unreadable store fails CLOSED (configured=true), matching
// HasAuthConfigured. Env credentials take precedence over file-backed ones.
func ResolveAuth(dataDir, raw string) (principal *Principal, configured bool) {
	env := envTokenCredentials()
	store, err := cachedTokenStore(dataDir)
	var file []credential
	if err != nil {
		configured = true // unreadable token store → assume configured (fail closed)
	} else {
		file = store.creds
		configured = len(env) > 0 || len(store.recs) > 0
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, configured
	}
	digest := sha256.Sum256([]byte(raw))
	envHit := matchCredential(env, digest)
	fileHit := matchCredential(file, digest)
	switch {
	case envHit != nil:
		return clonePrincipal(envHit.principal), configured
	case fileHit != nil && !tokenExpired(fileHit.expiresAt):
		return clonePrincipal(fileHit.principal), configured
	}
	return nil, configured // unknown, or known but expired
}

// clonePrincipal copies a cached principal so a caller cannot mutate the cache.
func clonePrincipal(p Principal) *Principal {
	p.Roles = slices.Clone(p.Roles)
	p.Workspaces = slices.Clone(p.Workspaces)
	return &p
}

// MintToken generates a non-expiring token for (id, role). See MintTokenTTL.
func MintToken(dataDir, id, role string) (string, error) {
	return MintTokenTTL(dataDir, id, role, 0)
}

// MintTokenTTL generates a token for (id, role), stores its hash, and returns the
// raw token once. Re-minting for an existing id replaces its token. ttlSeconds > 0
// sets an expiry (enforced in ResolveToken); 0 means the token never expires.
// validateMintInputs normalizes and validates id/role/ttl in place.
func validateMintInputs(id, role *string, ttlSeconds int) error {
	*id = strings.TrimSpace(*id)
	if err := validateSafeID("token", *id); err != nil {
		return fmt.Errorf("token id must be alphanumeric/._-: %w", err)
	}
	if IsOpenModeIdentity(*id) {
		// Every unauthenticated open-mode caller is this identity, so a principal
		// minted under it would pass as the author of all open-mode memory.
		return fmt.Errorf("token id %q is reserved for open mode", *id)
	}
	spec, err := ParseRoleSpec(*role)
	if err != nil {
		return err
	}
	*role = spec
	if ttlSeconds < 0 || ttlSeconds > maxTTLSeconds {
		return fmt.Errorf("ttl_seconds must be between 0 and %d", maxTTLSeconds)
	}
	return nil
}

func MintTokenTTL(dataDir, id, role string, ttlSeconds int) (string, error) {
	if err := validateMintInputs(&id, &role, ttlSeconds); err != nil {
		return "", err
	}
	tokenStoreMu.Lock()
	defer tokenStoreMu.Unlock()
	return mintTokenLocked(dataDir, id, role, ttlSeconds, nil)
}

// mintTokenLocked does the load-modify-write; the caller MUST hold tokenStoreMu.
// MintScopedToken mints a token confined to the given workspace ids (empty = all).
func MintScopedToken(dataDir, id, role string, ttlSeconds int, workspaces []string) (string, error) {
	if err := validateMintInputs(&id, &role, ttlSeconds); err != nil {
		return "", err
	}
	tokenStoreMu.Lock()
	defer tokenStoreMu.Unlock()
	return mintTokenLocked(dataDir, id, role, ttlSeconds, workspaces)
}

func mintTokenLocked(dataDir, id, role string, ttlSeconds int, workspaces []string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	raw := "xmt_" + base64.RawURLEncoding.EncodeToString(buf)
	recs, err := loadTokenRecords(dataDir)
	if err != nil {
		return "", err
	}
	next := make([]tokenRecord, 0, len(recs)+1)
	for _, r := range recs {
		if r.ID != id {
			next = append(next, r)
		}
	}
	expiresAt := ""
	if ttlSeconds > 0 {
		// nanosecond precision matches the After() comparison in tokenExpired, so a
		// short TTL doesn't live up to ~1s past its requested expiry.
		expiresAt = time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second).Format(time.RFC3339Nano)
	}
	next = append(next, tokenRecord{
		ID:          id,
		Role:        role,
		TokenSHA256: hashToken(raw),
		CreatedAt:   nowUTC(),
		ExpiresAt:   expiresAt,
		Workspaces:  workspaces,
	})
	if err := writeJSON(tokensPath(dataDir), next); err != nil {
		return "", err
	}
	invalidateTokenCache(dataDir)
	return raw, nil
}

// RotateToken issues a NEW secret for an existing principal, preserving its role,
// and invalidates the old secret (the old hash is overwritten). ttlSeconds applies
// to the new token. Errors with os.ErrNotExist if the id has no file-backed token
// (env-configured principals are static and cannot be rotated/revoked — manage them
// via the XMUSTARD_AUTH_TOKENS env var). The lookup and re-mint run under one lock
// so a concurrent revoke/mint can't race the rotation.
func RotateToken(dataDir, id string, ttlSeconds int) (string, error) {
	id = strings.TrimSpace(id)
	if ttlSeconds < 0 || ttlSeconds > maxTTLSeconds {
		return "", fmt.Errorf("ttl_seconds must be between 0 and %d", maxTTLSeconds)
	}
	tokenStoreMu.Lock()
	defer tokenStoreMu.Unlock()
	recs, err := loadTokenRecords(dataDir)
	if err != nil {
		return "", err
	}
	role := ""
	var workspaces []string
	found := false
	for _, r := range recs {
		if r.ID == id {
			role = fallbackString(r.Role, roleAgent)
			workspaces = r.Workspaces // preserve the workspace scope across rotation
			found = true
			break
		}
	}
	if !found {
		return "", os.ErrNotExist
	}
	return mintTokenLocked(dataDir, id, role, ttlSeconds, workspaces)
}

// ListPrincipals returns the configured principals and their roles (no secrets).
func ListPrincipals(dataDir string) []Principal {
	seen := map[string]Principal{}
	for _, c := range envTokenCredentials() {
		seen[c.principal.ID] = *clonePrincipal(c.principal)
	}
	if store, err := cachedTokenStore(dataDir); err == nil {
		for _, r := range store.recs {
			seen[r.ID] = newPrincipal(r.ID, fallbackString(r.Role, roleAgent), slices.Clone(r.Workspaces))
		}
	}
	out := make([]Principal, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RevokeToken removes a principal's token. Holds tokenStoreMu across the
// load-modify-write so a concurrent mint/rotate can't resurrect the revoked token.
func RevokeToken(dataDir, id string) error {
	tokenStoreMu.Lock()
	defer tokenStoreMu.Unlock()
	recs, err := loadTokenRecords(dataDir)
	if err != nil {
		return err
	}
	next := make([]tokenRecord, 0, len(recs))
	found := false
	for _, r := range recs {
		if r.ID == id {
			found = true
			continue
		}
		next = append(next, r)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := writeJSON(tokensPath(dataDir), next); err != nil {
		return err
	}
	invalidateTokenCache(dataDir)
	return nil
}
