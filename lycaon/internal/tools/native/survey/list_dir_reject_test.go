package survey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestListDirRejectsMissingPathWithStructuredCode(t *testing.T) {
	root := t.TempDir()
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "missing", "max_depth": 1}, nativefixture.Context(root))
	assertListDirRejectCode(t, err, "LIST_DIR_PATH_NOT_FOUND")
}

func TestListDirRejectsFileWithStructuredCode(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root, "source.go"), []byte("package source\n"), 0o644))
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "source.go", "max_depth": 1}, nativefixture.Context(root))
	assertListDirRejectCode(t, err, "LIST_DIR_NOT_DIRECTORY")
}

func assertListDirRejectCode(t *testing.T, err error, code string) {
	t.Helper()
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != code {
		t.Fatalf("error = %v, want %s ToolReject", err, code)
	}
}
