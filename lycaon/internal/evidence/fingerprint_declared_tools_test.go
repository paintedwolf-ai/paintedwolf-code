package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Every native tool the evidence catalog declares captures something citable
// from a minimal result: a body, a path or a URL.
func TestDeclaredNativeEvidenceToolsCaptureContent(t *testing.T) {
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	args := map[string]any{
		"path": "pkg/main.go", "paths": []any{"pkg/main.go"}, "path_a": "pkg/a.go", "path_b": "pkg/b.go",
		"dest": "pkg/out.json", "url": "https://docs.example.test/page",
	}
	content := `{"path":"pkg/main.go","url":"https://docs.example.test/page","results":[{"url":"https://docs.example.test/page"}],"anchors":[{"path":"pkg/main.go","line":3,"content":"func main() {}"}],"outcome":"accepted"}`
	native := 0
	for _, tool := range binding.DeclaredToolNames() {
		if ingestion.IsMCPToolName(tool) {
			continue
		}
		native++
		t.Run(tool, func(t *testing.T) {
			rec := evidence.BuildEvidenceRecord("", tool, args, content)
			if rec.Kind == "" {
				t.Fatal("declared tool built no record")
			}
			if len(rec.Body) == 0 && len(evidence.IndexedPathsForRecord(rec)) == 0 && len(evidence.ObservedURLsForRecord(rec)) == 0 {
				t.Fatalf("record captured nothing citable: %+v", rec)
			}
		})
	}
	if native == 0 {
		t.Fatal("evidence catalog declares no native tools")
	}
}
