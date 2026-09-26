package workspaceops

import (
	"fmt"
	"testing"
)

// BenchmarkRecall1k measures ranked recall over 1,000 promoted memories (WS-12: recall
// p50 on the store must not be worse than the JSON baseline).
func BenchmarkRecall1k(b *testing.B) {
	dir := b.TempDir()
	ws := "wsRecallBench"
	entries := make([]ContextEntry, 0, 1000)
	for i := 0; i < 1000; i++ {
		title := fmt.Sprintf("entry %d", i)
		content := fmt.Sprintf("memory body %d about widgets gadgets pipelines caches topic%d", i, i%50)
		entries = append(entries, ContextEntry{
			ID: fmt.Sprintf("b%04d", i), WorkspaceID: ws, Title: title, Content: content, Source: "author",
			Permission: "readonly", Status: "verified", Promoted: true, RequiredVerifications: 1,
			Verifications: []ContextVerification{{Agent: "peer", Approve: true, At: "2026-06-01T00:00:00Z"}},
			SearchTokens:  memoryTokenList(title + " " + content),
			CreatedAt:     fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
			UpdatedAt:     fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
		})
	}
	if err := saveContextEntries(dir, ws, entries); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := RecallContext(dir, ws, "gadgets topic7", nil, 8); err != nil {
			b.Fatal(err)
		}
	}
}
