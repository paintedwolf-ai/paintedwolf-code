package jq

import (
	"context"
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
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// TestJqDecompressesBlobstoreManagedSpill proves jq's read path decodes a
// blobstore-compressed body transparently rather than handing gojq raw zstd
// bytes to parse.
func TestJqDecompressesBlobstoreManagedSpill(t *testing.T) {
	dir := t.TempDir()
	host := t.TempDir()
	body := `{"name":"lycaon","version":3}`

	store := blobstore.Store{Root: host}
	blob, err := store.PutAt(tooloutput.ToolOutputSpillDir+"/spill.json", strings.NewReader(body), bytebound.Materialization(1<<20))
	testutil.FailErr(t, "spill via blobstore", err)

	raw, err := os.ReadFile(filepath.Join(host, filepath.FromSlash(blob.Rel)))
	testutil.FailErr(t, "read spilled file", err)
	if string(raw) == body {
		t.Fatal("expected on-disk spill body to be compressed, got plaintext")
	}

	ctx := testCtx(dir)
	ctx.HostDataDir = host
	tool := &Tool{Boundary: testBoundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": blob.Rel, "query": ".name"}, ctx)
	testutil.FailErr(t, "jq", err)
	resp := parseResponse(t, out)
	if len(resp.Values) != 1 || string(resp.Values[0]) != `"lycaon"` {
		t.Fatalf("jq did not read decoded plaintext JSON: values=%v raw=%s", resp.Values, out)
	}
}

// TestJqOrdinaryFileUnaffectedByBlobstoreCompression is the companion case:
// an ordinary project file, never touched by blobstore, reads identically.
func TestJqOrdinaryFileUnaffectedByBlobstoreCompression(t *testing.T) {
	dir := t.TempDir()
	writeJSONFile(t, dir, "data.json", `{"name":"lycaon","version":3}`)

	out, err := runJq(t, dir, map[string]any{"path": "data.json", "query": ".name"})
	testutil.FailErr(t, "jq", err)
	resp := parseResponse(t, out)
	if len(resp.Values) != 1 || string(resp.Values[0]) != `"lycaon"` {
		t.Fatalf("values = %v", resp.Values)
	}
}

func TestJqRejectsDecodedInputBeforeQueryPagination(t *testing.T) {
	dir, host := t.TempDir(), t.TempDir()
	body := `{"value":"` + strings.Repeat("x", int(safecmd.JQCaps().InputBytes)) + `"}`
	store := blobstore.Store{Root: host}
	blob, err := store.PutAt(tooloutput.ToolOutputSpillDir+"/oversized.json", strings.NewReader(body), bytebound.Materialization(len(body)+1))
	testutil.FailErr(t, "write compressed oversized input", err)
	ctx := testCtx(dir)
	ctx.HostDataDir = host
	ctx.MaxToolSpillBytes = int(safecmd.JQCaps().InputBytes)
	tool := &Tool{Boundary: testBoundary(t)}
	out, err := tool.Run(t.Context(), map[string]any{"path": blob.Rel, "query": ".value", "offset": 0, "limit": 1}, ctx)
	var reject *tools.ToolReject
	if out != "" || !errors.As(err, &reject) || reject.Code != "JQ_INPUT_TOO_LARGE" || reject.Data["max_bytes"] != safecmd.JQCaps().InputBytes {
		t.Fatalf("oversized decoded input reached query: len=%d err=%v", len(out), err)
	}
}

func TestJqSlicesLargeRetainedStringsWithinTheSpillBound(t *testing.T) {
	dir, host := t.TempDir(), t.TempDir()
	body := `{"value":"` + strings.Repeat("x", int(safecmd.JQCaps().InputBytes)) + `original tail"}`
	spill := tooloutput.SpillWholeToolOutput(host, tooloutput.Screened(body), 0)
	if spill.SpillPath == "" {
		t.Fatal("failed to retain large structured observation")
	}
	ctx := testCtx(dir)
	ctx.HostDataDir = host
	tool := &Tool{Boundary: testBoundary(t)}
	for _, cap := range []int{0, len(body) + 100} {
		ctx.MaxToolSpillBytes = cap
		out, err := tool.Run(t.Context(), map[string]any{"path": spill.SpillPath, "query": ".value[-13:]"}, ctx)
		testutil.FailErr(t, "slice retained observation", err)
		resp := parseResponse(t, out)
		if len(resp.Values) != 1 || string(resp.Values[0]) != `"original tail"` {
			t.Fatalf("lost retained substring: %s", out)
		}
	}
	writeJSONFile(t, dir, "ordinary.json", body)
	_, err := tool.Run(t.Context(), map[string]any{"path": "ordinary.json", "query": ".value[-13:]"}, ctx)
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "JQ_INPUT_TOO_LARGE" || reject.Data["max_bytes"] != safecmd.JQCaps().InputBytes {
		t.Fatalf("spill allowance leaked into project query: %v", err)
	}
}
