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
)

// Bearer-token auth for self-hosted deployments. Each principal gets its own token
// mapped to an identity; the verify path authenticates via that token rather than a
// caller-asserted string, so one token cannot stand in for multiple distinct
// identities. Tokens are stored hashed (sha256) at rest; the raw token is returned
// once at mint time and is not recoverable.

type Principal struct {
	ID   string `json:"id"`
	Role string `json:"role"` // admin | agent | readonly
}

type tokenRecord struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	TokenSHA256 string `json:"token_sha256"`
	CreatedAt   string `json:"created_at"`
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
		out[hashToken(tok)] = Principal{ID: id, Role: fallbackString(role, "agent")}
	}
	return out
}

// HasAuthConfigured reports whether any tokens exist (file or env) — i.e. whether
// auth should be enforced in "auto" mode.
func HasAuthConfigured(dataDir string) bool {
	if len(envTokenHashes()) > 0 {
		return true
	}
	recs, _ := loadTokenRecords(dataDir)
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
			return &Principal{ID: r.ID, Role: fallbackString(r.Role, "agent")}
		}
	}
	return nil
}

// MintToken generates a token for (id, role), stores its hash, and returns the raw
// token once. Re-minting for an existing id replaces its token.
func MintToken(dataDir, id, role string) (string, error) {
	id = strings.TrimSpace(id)
	if err := validateSafeID("token", id); err != nil {
		return "", fmt.Errorf("token id must be alphanumeric/._-: %w", err)
	}
	role = fallbackString(strings.TrimSpace(role), "agent")
	switch role {
	case "admin", "agent", "readonly":
	default:
		return "", fmt.Errorf("role must be admin|agent|readonly")
	}
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
	next = append(next, tokenRecord{ID: id, Role: role, TokenSHA256: hashToken(raw), CreatedAt: nowUTC()})
	if err := writeJSON(tokensPath(dataDir), next); err != nil {
		return "", err
	}
	return raw, nil
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

// RevokeToken removes a principal's token.
func RevokeToken(dataDir, id string) error {
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
