package contract

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func nativeWriteTestZipFile(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		testutil.FailErr(t, "write zip", err)
	}
}

// extractArchiveRefusal extracts archive into dest inside a project nested one level below
// base, so a write that escapes the project lands in base where the test can see it.
func extractArchiveRefusal(t *testing.T, entries map[string]string, dest string) (base, root string, reject *toolrejection.ToolReject) {
	t.Helper()
	base = t.TempDir()
	root = filepath.Join(base, "project")
	testutil.FailErr(t, "mkdir project", os.Mkdir(root, 0o755))
	nativeWriteTestZipFile(t, filepath.Join(root, "in.zip"), entries)
	tool := &native.ExtractArchiveTool{Boundary: contractcheck.ProdToolBoundary(t)}
	_, err := tool.Run(t.Context(), map[string]any{"path": "in.zip", "dest": dest}, implementToolContext(root))
	if !errors.As(err, &reject) {
		t.Fatalf("extract = %v, want a tool rejection", err)
	}
	return base, root, reject
}

func assertAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a refused operation (%v)", path, err)
		}
	}
}

func TestExtractArchiveZipSlipBlockedSecurity(t *testing.T) {
	t.Parallel()
	base, root, reject := extractArchiveRefusal(t, map[string]string{"../../outside.txt": "x"}, "out")
	if reject.Code != "EXTRACT_ZIP_SLIP" || reject.Data["entry"] != "../../outside.txt" {
		t.Fatalf("reject = %s %v, want EXTRACT_ZIP_SLIP for the escaping entry", reject.Code, reject.Data)
	}
	assertAbsent(t, filepath.Join(base, "outside.txt"), filepath.Join(root, "outside.txt"), filepath.Join(root, "out", "outside.txt"))
}

func TestExtractArchiveSizeCapBlockedSecurity(t *testing.T) {
	t.Parallel()
	const entryBytes = 4 << 20
	_, root, reject := extractArchiveRefusal(t, map[string]string{"huge.bin": strings.Repeat("z", entryBytes)}, "out")
	if reject.Code != "EXTRACT_SIZE_EXCEEDED" {
		t.Fatalf("reject = %s %v, want EXTRACT_SIZE_EXCEEDED", reject.Code, reject.Data)
	}
	limit, ok := reject.Data["max_uncompressed_bytes"].(int64)
	if !ok || limit <= 0 || limit >= entryBytes {
		t.Fatalf("max_uncompressed_bytes = %v, want a positive limit below the %d-byte entry", reject.Data["max_uncompressed_bytes"], entryBytes)
	}
	assertAbsent(t, filepath.Join(root, "out", "huge.bin"))
}

func TestExtractArchiveDestEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	base, _, reject := extractArchiveRefusal(t, map[string]string{"a.txt": "ok"}, "../outside")
	if reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("reject = %s %v, want SURVEY_PATH_ESCAPE", reject.Code, reject.Data)
	}
	assertAbsent(t, filepath.Join(base, "outside"))
}
