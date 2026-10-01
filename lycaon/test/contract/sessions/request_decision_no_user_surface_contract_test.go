package contract

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRequestDecisionNeverCreatesUserFacingSurface(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "native", "workercontrol", "request_decision.go")
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read request_decision.go", err)
	src := string(raw)

	forbidden := []string{
		"RequestCheckpoint",
		"RequestUserInput",
		"announceFeedbackToTranscript",
		"MessageKindWorkflowFeedback",
		"hitl.",
	}
	for _, needle := range forbidden {
		if strings.Contains(src, needle) {
			t.Fatalf("request_decision.go must not reference %q (user-facing checkpoint/feedback bus)", needle)
		}
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, raw, parser.ImportsOnly)
	contractcheck.FailErr(t, "parse request_decision.go", err)
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(path, "/internal/hitl") || strings.HasSuffix(path, "/internal/workflow") {
			t.Fatalf("request_decision.go must not import %s", path)
		}
	}
}
