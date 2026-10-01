package survey

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestReadAndWcShareLineCount(t *testing.T) {
	tmpDir := t.TempDir()
	content := "a\nb\nc\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte(content), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	boundary := nativefixture.Boundary(t)
	ctx := nativefixture.Context(tmpDir)
	readOut, err := (&ReadTool{Boundary: boundary}).Run(context.Background(), map[string]any{"path": "f.txt"}, ctx)
	testutil.FailErr(t, "read", err)
	var readResp ReadResponse
	testutil.FailErr(t, "decode read", json.Unmarshal([]byte(readOut), &readResp))
	wcOut, err := (&WcTool{Boundary: boundary}).Run(context.Background(), map[string]any{"paths": []any{"f.txt"}}, ctx)
	testutil.FailErr(t, "wc", err)
	var wcResp wcResponse
	testutil.FailErr(t, "decode wc", json.Unmarshal([]byte(wcOut), &wcResp))
	if readResp.TotalLines != 3 || wcResp.Results[0].Lines == nil || *wcResp.Results[0].Lines != 3 {
		t.Fatalf("read total=%d wc=%v", readResp.TotalLines, wcResp.Results[0].Lines)
	}
}

func TestGrepGoDefinitionPatternAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\nfunc Foo() {}\ntype Bar struct{}\n"), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"pattern": `(?:func|type) \w+\s*(?:\([^)]*\))?\s*(?:\{|: func)`,
	}, nativefixture.Context(tmpDir))
	if err != nil {
		t.Fatalf("expected pattern to compile, got %v", err)
	}
}

func TestFindEmptyResultNoDeeperPathsOmitted(t *testing.T) {
	tmpDir := t.TempDir()
	sub := filepath.Join(tmpDir, "lycaon-den", "src", "nested", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":      "lycaon-den",
		"name_glob": "*.go",
		"type":      "file",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find", err)
	resp := parseFindResponse(t, out)
	if resp.DeeperPathsOmitted {
		t.Fatalf("expected no deeper_paths_omitted on empty results: %+v", resp)
	}
}

func TestWcReportsTrueSizeWhenScanTruncated(t *testing.T) {
	prev := hostGrepMaxFileBytes
	hostGrepMaxFileBytes = 32 // tiny cap — avoid multi-MiB fixtures on disk
	t.Cleanup(func() { hostGrepMaxFileBytes = prev })

	tmpDir := t.TempDir()
	body := strings.Repeat("line\n", 40) // 200 bytes, well past the 32-byte cap
	testutil.FailErr(t, "write big.txt", os.WriteFile(filepath.Join(tmpDir, "big.txt"), []byte(body), 0o644))

	out, err := (&WcTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(),
		map[string]any{"paths": []any{"big.txt"}}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "wc", err)
	var resp wcResponse
	testutil.FailErr(t, "decode wc", json.Unmarshal([]byte(out), &resp))

	got := resp.Results[0]
	// Bytes is the real file size, not the scanned prefix — wc counting is its job.
	if got.Bytes != int64(len(body)) {
		t.Fatalf("bytes = %d want %d (true size, not the %d-byte scan prefix)",
			got.Bytes, len(body), hostGrepMaxFileBytes)
	}
	// Partial line counts are declared, not passed off as complete.
	if !got.Truncated {
		t.Fatal("truncated = false; capped scan must be reported")
	}
}
