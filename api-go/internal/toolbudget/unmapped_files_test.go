package toolbudget

import (
	"fmt"
	"testing"
)

func TestImpactScopeBudgetsUnmappedFilesAtSmallAndDefaultLimits(t *testing.T) {
	files := make([]string, 200)
	for i := range files {
		files[i] = fmt.Sprintf("pkg/generated/source-file-%03d-with-a-long-name.ts", i)
	}
	result := map[string]any{"changed_symbols": []any{}, "unmapped_files": files, "unmapped_files_total": len(files)}
	for _, maxChars := range []int{MinMaxChars, 8000} {
		got := run(t, "impact", result, Request{MaxChars: maxChars}, Page{}, nil)
		if len(got.raw) > maxChars {
			t.Fatalf("max_chars=%d: output is %d bytes", maxChars, len(got.raw))
		}
		if got.out["unmapped_files_total"] != float64(len(files)) {
			t.Fatalf("max_chars=%d: total was lost: %v", maxChars, got.out)
		}
		kept, ok := got.out["unmapped_files"].([]any)
		if !ok || len(kept) >= len(files) || len(kept) == 0 {
			t.Fatalf("max_chars=%d: unmapped files were not fitted: %v", maxChars, got.out)
		}
	}
}
