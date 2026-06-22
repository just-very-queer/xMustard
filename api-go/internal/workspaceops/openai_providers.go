package workspaceops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// SSRF guard. Provider URLs trigger server-side HTTP requests with the operator's
// bearer key attached, so a malicious base_url could probe internal services or
// the cloud-metadata endpoint. Loopback and RFC1918 are intentionally ALLOWED —
// local model servers (Ollama/vLLM/LM Studio) are the whole point and live there —
// but link-local (incl. 169.254.169.254 metadata) and known metadata IPs are
// blocked at dial time (after DNS resolution, defeating rebinding), and redirects
// are not followed.
var metadataIPs = map[string]struct{}{
	"169.254.169.254": {}, // AWS/GCP/Azure IMDS
	"fd00:ec2::254":   {}, // AWS IMDS over IPv6
	"100.100.100.200": {}, // Alibaba metadata
}

func blockedHostIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if _, bad := metadataIPs[ip.String()]; bad {
		return true
	}
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

var providerHTTPClient = &http.Client{
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 30 * time.Second,
			Control: func(_ string, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					host = address
				}
				if blockedHostIP(net.ParseIP(host)) {
					return fmt.Errorf("blocked link-local/metadata address %s", host)
				}
				return nil
			},
		}).DialContext,
	},
}

// rejectBlockedURLHost rejects a base_url whose host is a blocked IP literal
// (fast feedback at add-time; the dialer Control catches DNS-resolved cases).
func rejectBlockedURLHost(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid base_url: %w", err)
	}
	if blockedHostIP(net.ParseIP(u.Hostname())) {
		return fmt.Errorf("base_url host %s is a blocked (link-local/metadata) address", u.Hostname())
	}
	return nil
}

// OpenAI-compatible provider integration. xMustard's local agents are CLIs
// (codex/opencode); this adds first-class access to any OpenAI-compatible HTTP
// endpoint — Ollama, vLLM, LM Studio, OpenAI itself, and vision (VLM) models —
// so the runtime can read/serve models without a bespoke client per vendor.
//
// Security: we never store API keys. A provider records the NAME of an
// environment variable (APIKeyEnv); the key is read from the process env at call
// time. This keeps secrets out of providers.json (which is plain config).

type OpenAIProvider struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"` // ollama | openai | vllm | lmstudio | custom
	BaseURL        string `json:"base_url"`
	APIKeyEnv      string `json:"api_key_env,omitempty"` // env var holding the key, NOT the key
	DefaultModel   string `json:"default_model,omitempty"`
	SupportsVision bool   `json:"supports_vision"`
	CreatedAt      string `json:"created_at"`
}

func providersPath(dataDir string) string {
	return filepath.Join(dataDir, "providers.json")
}

func loadProviders(dataDir string) ([]OpenAIProvider, error) {
	var providers []OpenAIProvider
	if err := readJSON(providersPath(dataDir), &providers); err != nil {
		if os.IsNotExist(err) {
			return []OpenAIProvider{}, nil
		}
		return nil, err
	}
	return providers, nil
}

// ListOpenAIProviders returns the configured providers (secrets are never stored).
func ListOpenAIProviders(dataDir string) ([]OpenAIProvider, error) {
	return loadProviders(dataDir)
}

// known base URLs by kind, used when the caller omits base_url.
func defaultBaseURL(kind string) string {
	switch kind {
	case "ollama":
		return "http://localhost:11434/v1"
	case "vllm":
		return "http://localhost:8000/v1"
	case "lmstudio":
		return "http://localhost:1234/v1"
	case "openai":
		return "https://api.openai.com/v1"
	default:
		return ""
	}
}

// AddOpenAIProvider validates and upserts a provider (by name).
func AddOpenAIProvider(dataDir string, p OpenAIProvider) (*OpenAIProvider, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, fmt.Errorf("provider name is required")
	}
	p.Kind = strings.ToLower(strings.TrimSpace(p.Kind))
	if p.Kind == "" {
		p.Kind = "custom"
	}
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if p.BaseURL == "" {
		p.BaseURL = defaultBaseURL(p.Kind)
	}
	if p.BaseURL == "" {
		return nil, fmt.Errorf("base_url is required for kind %q", p.Kind)
	}
	if !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
		return nil, fmt.Errorf("base_url must be http(s)")
	}
	if err := rejectBlockedURLHost(p.BaseURL); err != nil {
		return nil, err
	}
	if p.CreatedAt == "" {
		p.CreatedAt = nowUTC()
	}
	providers, err := loadProviders(dataDir)
	if err != nil {
		return nil, err
	}
	next := make([]OpenAIProvider, 0, len(providers)+1)
	for _, existing := range providers {
		if !strings.EqualFold(existing.Name, p.Name) {
			next = append(next, existing)
		}
	}
	next = append(next, p)
	sort.Slice(next, func(i, j int) bool { return next[i].Name < next[j].Name })
	if err := writeJSON(providersPath(dataDir), next); err != nil {
		return nil, err
	}
	return &p, nil
}

// RemoveOpenAIProvider deletes a provider by name.
func RemoveOpenAIProvider(dataDir, name string) error {
	providers, err := loadProviders(dataDir)
	if err != nil {
		return err
	}
	next := make([]OpenAIProvider, 0, len(providers))
	found := false
	for _, existing := range providers {
		if strings.EqualFold(existing.Name, name) {
			found = true
			continue
		}
		next = append(next, existing)
	}
	if !found {
		return os.ErrNotExist
	}
	return writeJSON(providersPath(dataDir), next)
}

func findProvider(dataDir, name string) (*OpenAIProvider, error) {
	providers, err := loadProviders(dataDir)
	if err != nil {
		return nil, err
	}
	for i := range providers {
		if strings.EqualFold(providers[i].Name, name) {
			return &providers[i], nil
		}
	}
	return nil, os.ErrNotExist
}

func (p *OpenAIProvider) apiKey() string {
	if p.APIKeyEnv == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(p.APIKeyEnv))
}

func (p *OpenAIProvider) httpRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := p.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req, nil
}

// ProviderModel is one model id advertised by a provider's /models endpoint.
type ProviderModel struct {
	ID string `json:"id"`
}

// OpenAIEmbeddings POSTs {base}/embeddings and returns one vector per input. Used
// by the neural search-rerank lane; any OpenAI-compatible embeddings model works
// (e.g. Ollama's nomic-embed-text), so it stays Python-free.
func OpenAIEmbeddings(dataDir, name, model string, inputs []string) ([][]float64, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	provider, err := findProvider(dataDir, name)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(model) == "" {
		model = provider.DefaultModel
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": inputs})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := provider.httpRequest(ctx, http.MethodPost, "/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider %s unreachable at %s: %w", name, provider.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("provider %s embeddings -> %d: %s", name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode embeddings: %w", err)
	}
	out := make([][]float64, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// ListProviderModels GETs {base}/models from an OpenAI-compatible endpoint.
func ListProviderModels(dataDir, name string) (map[string]any, error) {
	provider, err := findProvider(dataDir, name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := provider.httpRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider %s unreachable at %s: %w", name, provider.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("provider %s /models -> %d: %s", name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Data []ProviderModel `json:"data"`
	}
	_ = json.Unmarshal(raw, &parsed)
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		ids = append(ids, m.ID)
	}
	return map[string]any{
		"provider": name,
		"base_url": provider.BaseURL,
		"models":   ids,
		"count":    len(ids),
	}, nil
}

// ProbeOpenAIProvider checks connectivity (and whether an API key is present).
func ProbeOpenAIProvider(dataDir, name string) (map[string]any, error) {
	provider, err := findProvider(dataDir, name)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"provider":        name,
		"base_url":        provider.BaseURL,
		"kind":            provider.Kind,
		"supports_vision": provider.SupportsVision,
		"api_key_present": provider.apiKey() != "",
		"checked_at":      nowUTC(),
	}
	models, err := ListProviderModels(dataDir, name)
	if err != nil {
		result["ok"] = false
		result["error"] = err.Error()
		return result, nil
	}
	result["ok"] = true
	result["model_count"] = models["count"]
	return result, nil
}

// chatContentPart is an OpenAI chat content part (text or image_url) for VLMs.
type chatContentPart struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	ImageURL map[string]any `json:"image_url,omitempty"`
}

// ChatRequest is the input to OpenAIChat.
type ChatRequest struct {
	Model     string   `json:"model"`
	Prompt    string   `json:"prompt"`
	System    string   `json:"system,omitempty"`
	ImageURLs []string `json:"image_urls,omitempty"` // http(s) or data: URLs for VLMs
	ImagePath string   `json:"image_path,omitempty"` // local image file, encoded to data URL
}

// OpenAIChat calls {base}/chat/completions; supports vision via image parts.
func OpenAIChat(dataDir, name string, req ChatRequest) (map[string]any, error) {
	provider, err := findProvider(dataDir, name)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = provider.DefaultModel
	}
	if model == "" {
		return nil, fmt.Errorf("model is required (provider %s has no default_model)", name)
	}

	// Build the user message: plain text, or multimodal parts when images present.
	images := append([]string{}, req.ImageURLs...)
	if strings.TrimSpace(req.ImagePath) != "" {
		// image_path is confined to the uploads root: an agent must not be able to
		// make the server read+exfiltrate arbitrary host files (e.g. /etc/passwd,
		// SSH keys) to a remote provider (XM-NEW-014).
		dataURL, err := imageFileToDataURL(filepath.Join(dataDir, "uploads"), req.ImagePath)
		if err != nil {
			return nil, err
		}
		images = append(images, dataURL)
	}
	var userContent any
	if len(images) > 0 {
		if !provider.SupportsVision {
			return nil, fmt.Errorf("provider %s is not marked supports_vision", name)
		}
		parts := []chatContentPart{{Type: "text", Text: req.Prompt}}
		for _, url := range images {
			parts = append(parts, chatContentPart{Type: "image_url", ImageURL: map[string]any{"url": url}})
		}
		userContent = parts
	} else {
		userContent = req.Prompt
	}

	messages := []map[string]any{}
	if strings.TrimSpace(req.System) != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	messages = append(messages, map[string]any{"role": "user", "content": userContent})

	payload := map[string]any{"model": model, "messages": messages, "stream": false}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	httpReq, err := provider.httpRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp, err := providerHTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider %s unreachable at %s: %w", name, provider.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("provider %s chat -> %d: %s", name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode chat response: %w", err)
	}
	content := ""
	if len(parsed.Choices) > 0 {
		content = parsed.Choices[0].Message.Content
	}
	return map[string]any{
		"provider":  name,
		"model":     model,
		"content":   content,
		"vision":    len(images) > 0,
		"served_by": parsed.Model,
	}, nil
}

// imageFileToDataURL reads a confined, size-capped regular file under `root` and
// returns a base64 data URL. `rel` must be a workspace-relative path beneath root
// (no absolute paths, `..` traversal, or symlink escape).
func imageFileToDataURL(root, rel string) (string, error) {
	data, ok := readWorkspaceRegularFile(root, rel)
	if !ok {
		return "", fmt.Errorf("image must be a regular file under the uploads directory within the size limit")
	}
	mime := "image/png"
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".gif":
		mime = "image/gif"
	case ".webp":
		mime = "image/webp"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
