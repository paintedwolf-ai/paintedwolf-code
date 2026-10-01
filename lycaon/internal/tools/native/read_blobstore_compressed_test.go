package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// TestReadToolDecompressesBlobstoreManagedSpill proves the model-facing read
// path: content written through blobstore.Store (compressed on disk) comes
// back as plaintext through the real read tool, not raw zstd bytes.
func TestReadToolDecompressesBlobstoreManagedSpill(t *testing.T) {
	tmpDir := t.TempDir()
	host := t.TempDir()
	plain := strings.Repeat("compressed tool output line\n", 500)

	store := blobstore.Store{Root: host}
	blob, err := store.PutAt(tooloutput.ToolOutputSpillRelPath(plain), strings.NewReader(plain), bytebound.Materialization(10<<20))
	testutil.FailErr(t, "spill via blobstore", err)

	// The read below exercises decompression only if the stored bytes are compressed.
	raw, err := os.ReadFile(filepath.Join(host, filepath.FromSlash(blob.Rel)))
	testutil.FailErr(t, "read spilled file", err)
	if string(raw) == plain {
		t.Fatal("expected on-disk spill body to be compressed, got plaintext")
	}

	ctx := nativefixture.Context(tmpDir)
	ctx.HostDataDir = host
	tool := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": blob.Rel}, ctx)
	testutil.FailErr(t, "tool.Run failed", err)

	var resp surveytools.ReadResponse
	testutil.FailErr(t, "decode read", json.Unmarshal([]byte(out), &resp))
	if !strings.Contains(resp.Content, "compressed tool output line") {
		t.Fatalf("read tool did not return decoded plaintext: %q", resp.Content)
	}
}

// TestReadToolOrdinaryFileUnaffectedByBlobstoreCompression verifies an ordinary
// project file unmanaged by blobstore returns its raw disk bytes.
func TestReadToolOrdinaryFileUnaffectedByBlobstoreCompression(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(tmpDir, "plain.txt"), []byte("hello world"), 0o644))

	tool := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "plain.txt"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "tool.Run failed", err)

	var resp surveytools.ReadResponse
	testutil.FailErr(t, "decode read", json.Unmarshal([]byte(out), &resp))
	if resp.Content != "     1: hello world" {
		t.Fatalf("ordinary file read regressed: content=%q", resp.Content)
	}
}

func TestReadRetainedDiffPagesIgnoreLaterWorkspaceChanges(t *testing.T) {
	root, host := t.TempDir(), t.TempDir()
	diff := strings.Repeat("+original line\n", 2500) + "+original final line\n"
	spill := tooloutput.SpillWholeRaw(host, tooloutput.Screened(diff), 0)
	if spill.SpillPath == "" {
		t.Fatal("failed to retain diff")
	}
	testutil.FailErr(t, "replace working file", os.WriteFile(filepath.Join(root, "file.go"), []byte("completely different now\n"), 0o644))
	ctx := nativefixture.Context(root)
	ctx.HostDataDir = host
	tool := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(t.Context(), map[string]any{"path": spill.SpillPath, "offset": 2500, "limit": 2}, ctx)
	testutil.FailErr(t, "read captured hunk tail", err)
	var response surveytools.ReadResponse
	testutil.FailErr(t, "decode captured tail", json.Unmarshal([]byte(out), &response))
	if !strings.Contains(response.Content, "  2501: +original final line") || strings.Contains(response.Content, "completely different") {
		t.Fatalf("did not recover captured hunks: %s", response.Content)
	}
}

func TestReadLargeToolSpillUsesRetentionBound(t *testing.T) {
	root, host := t.TempDir(), t.TempDir()
	plain := strings.Repeat(strings.Repeat("x", 4095)+"\n", 2050) + "retained tail\n"
	spill := tooloutput.SpillWholeRaw(host, tooloutput.Screened(plain), 0)
	if spill.SpillPath == "" || len(plain) <= readcaps.MaxFileBytes {
		t.Fatal("fixture must retain an observation above the project-file limit")
	}
	ctx := editorCtx(root, &fakeEditorDocuments{openErr: errors.New("spill reads must not consult project editor documents")})
	ctx.HostDataDir = host
	tool := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	for _, limit := range []int{0, len(plain)} {
		ctx.MaxToolSpillBytes = limit
		out, err := tool.Run(t.Context(), map[string]any{"path": spill.SpillPath, "offset": 2051, "limit": 1}, ctx)
		testutil.FailErr(t, "read large retained observation", err)
		var response surveytools.ReadResponse
		testutil.FailErr(t, "decode retained page", json.Unmarshal([]byte(out), &response))
		if response.Content != "  2051: retained tail" {
			t.Fatalf("lost retained tail: %q", response.Content)
		}
	}
	ctx.MaxToolSpillBytes = len(plain) - 1
	_, err := tool.Run(t.Context(), map[string]any{"path": spill.SpillPath, "offset": 2051, "limit": 1}, ctx)
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "READ_FILE_TOO_LARGE" || reject.Data["max_file_bytes"] != int64(len(plain)-1) {
		t.Fatalf("retention read ignored configured bound: %v", err)
	}
	// A matching directory name in a project is not host spill authority.
	ctx.HostDataDir = ""
	ctx.EditorDocuments = nil
	ctx.MaxToolSpillBytes = tooloutput.DefaultMaxSpillFileBytes
	path := filepath.Join(root, "tool-output", "project.txt")
	testutil.FailErr(t, "create project directory", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write project fixture", os.WriteFile(path, []byte(plain), 0o600))
	_, err = tool.Run(t.Context(), map[string]any{"path": "tool-output/project.txt", "offset": 2051, "limit": 1}, ctx)
	if !errors.As(err, &reject) || reject.Code != "READ_FILE_TOO_LARGE" || reject.Data["max_file_bytes"] != int64(readcaps.MaxFileBytes) {
		t.Fatalf("spill allowance leaked into project-file reads: %v", err)
	}
}
