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
	"sort"
	"strings"
	"sync"
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
	ID   string `json:"id"`
	Role string `json:"role"` // admin | agent | readonly
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

// envTokenHashes parses XMUSTARD_AUTH_TOKENS="id:role:rawtoken,..." into hash→Principal
// (the raw tokens are hashed in memory and never persisted).
func envTokenHashes() map[string]Principal {
	out := map[string]Principal{}
	raw := strings.TrimSpace(os.Getenv("XMUSTARD_AUTH_TOKENS"))
	if raw == "" {
		return out
	}
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
		out[hashToken(tok)] = Principal{ID: id, Role: normalizeRole(role)}
	}
	return out
}

// normalizeRole maps a role string to a known role, defaulting unknown/blank to
// the least-privileged "readonly" so a typo in XMUSTARD_AUTH_TOKENS can't mint an
// unconstrained principal (an unknown role would otherwise dodge both the
// readonly-method gate and the role-hierarchy gate).
func normalizeRole(role string) string {
	switch strings.TrimSpace(role) {
	case "admin":
		return "admin"
	case "agent":
		return "agent"
	case "readonly":
		return "readonly"
	case "":
		return "agent" // historical default for a blank role
	default:
		return "readonly"
	}
}

// HasAuthConfigured reports whether any tokens exist (file or env) — i.e. whether
// auth should be enforced in "auto" mode. Fails CLOSED: if the token store exists
// but can't be read/parsed (corruption, bad perms), it returns true so a damaged
// file can't silently disable auth and open every admin gate. A genuinely-absent
// file (os.IsNotExist) correctly reports false.
func HasAuthConfigured(dataDir string) bool {
	if len(envTokenHashes()) > 0 {
		return true
	}
	recs, err := loadTokenRecords(dataDir)
	if err != nil {
		return true // unreadable token store → assume auth is configured (fail closed)
	}
	return len(recs) > 0
}

func constantTimeEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ResolveToken returns the Principal for a raw bearer token, or nil if unknown.
func ResolveToken(dataDir, raw string) *Principal {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	h := hashToken(raw)
	if p, ok := envTokenHashes()[h]; ok {
		return &p
	}
	recs, _ := loadTokenRecords(dataDir)
	for _, r := range recs {
		if constantTimeEqual(r.TokenSHA256, h) {
			if tokenExpired(r.ExpiresAt) {
				return nil // a known-but-expired token resolves to no principal
			}
			return &Principal{ID: r.ID, Role: fallbackString(r.Role, "agent"), Workspaces: r.Workspaces}
		}
	}
	return nil
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
	*role = fallbackString(strings.TrimSpace(*role), "agent")
	switch *role {
	case "admin", "agent", "readonly":
	default:
		return fmt.Errorf("role must be admin|agent|readonly")
	}
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
			role = fallbackString(r.Role, "agent")
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

// ListPrincipals returns the configured principals (no secrets).
func ListPrincipals(dataDir string) []Principal {
	seen := map[string]Principal{}
	for _, p := range envTokenHashes() {
		seen[p.ID] = p
	}
	recs, _ := loadTokenRecords(dataDir)
	for _, r := range recs {
		seen[r.ID] = Principal{ID: r.ID, Role: fallbackString(r.Role, "agent")}
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
	return writeJSON(tokensPath(dataDir), next)
}
