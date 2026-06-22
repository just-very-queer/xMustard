package workspaceops

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIEmbeddingsAndCosine(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/embeddings") {
			http.Error(w, "nf", http.StatusNotFound)
			return
		}
		var body struct {
			Input []string `json:"input"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		// deterministic toy embedding: vector length encodes the first rune.
		data := make([]map[string]any, len(body.Input))
		for i, s := range body.Input {
			var first float64
			if len(s) > 0 {
				first = float64(s[0])
			}
			data[i] = map[string]any{"embedding": []float64{first, 1.0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	if _, err := AddOpenAIProvider(dir, OpenAIProvider{
		Name: "emb", Kind: "ollama", BaseURL: srv.URL, DefaultModel: "nomic-embed-text",
	}); err != nil {
		t.Fatal(err)
	}

	vecs, err := OpenAIEmbeddings(dir, "emb", "", []string{"alpha", "beta"})
	if err != nil || len(vecs) != 2 || len(vecs[0]) != 2 {
		t.Fatalf("embeddings: %v %v", err, vecs)
	}

	// cosine of identical vectors is 1; orthogonal-ish is < 1.
	if c := cosineFloat(vecs[0], vecs[0]); c < 0.999 {
		t.Fatalf("self-cosine should be ~1, got %v", c)
	}
	if cosineFloat([]float64{1, 0}, []float64{0, 1}) != 0 {
		t.Fatalf("orthogonal cosine should be 0")
	}
}

func TestRankIndicesOrders(t *testing.T) {
	scores := []float64{0.1, 0.9, 0.5}
	r := rankIndices(len(scores), func(i int) float64 { return scores[i] })
	if r[0] != 1 || r[1] != 2 || r[2] != 0 {
		t.Fatalf("expected order [1 2 0], got %v", r)
	}
}
