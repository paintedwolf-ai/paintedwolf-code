package survey

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReadTool(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "hello.txt"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "tool.Run failed", err)
	nativefixture.AssertReceipt(t, out)
	content := nativefixture.SurveyContent(t, out)
	var resp ReadResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		testutil.FailErr(t, "decode read", err)
	}
	if resp.TotalLines != 1 || resp.Content != "     1: hello world" || resp.Offset != 1 {
		t.Fatalf("resp = %+v content=%q", resp, content)
	}
}

func TestReadToolPreservesLiteralHTMLInJSON(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "tpl.html"), []byte("<div>&amp;</div>"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "tpl.html"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "tool.Run failed", err)
	if strings.Contains(out, `\u003c`) {
		t.Fatalf("JSON should not unicode-escape angle brackets: %s", out)
	}
	var resp ReadResponse
	testutil.FailErr(t, "decode read", json.Unmarshal([]byte(out), &resp))
	if resp.Content != "     1: <div>&amp;</div>" {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestReadToolNumberedOffset(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "lines.txt"), []byte("a\nb\nc\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "lines.txt", "offset": 2, "limit": 1}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "tool.Run failed", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode read", json.Unmarshal([]byte(out), &resp))
	if resp.Content != "     2: b" || resp.EndLine != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestReadToolCoordinatorGetsOutlineOnLargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	var b strings.Builder
	b.WriteString("package big\n\nfunc Marker() {}\n")
	for i := 0; i < readcaps.AutoOutlineThreshold+10; i++ {
		b.WriteString("// filler line\n")
	}
	testutil.FailErr(t, "write big file", os.WriteFile(filepath.Join(tmpDir, "big.go"), []byte(b.String()), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "big.go"}, nativefixture.AgentContext(tmpDir, "coordinator"))
	testutil.FailErr(t, "coordinator read", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode read", json.Unmarshal([]byte(out), &resp))
	if resp.Mode != "outline" {
		t.Fatalf("coordinator large-file read mode = %q want outline", resp.Mode)
	}
	if !strings.Contains(out, "Marker") {
		t.Fatalf("outline should list the Marker symbol: %q", out)
	}
}

func TestReadToolRejectsBinary(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "bin.dat"), []byte("a\x00b"), 0o644); err != nil {
		testutil.FailErr(t, "write binary", err)
	}
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "bin.dat"}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "READ_BINARY_DENIED" {
		t.Fatalf("err = %v want READ_BINARY_DENIED", err)
	}
}

func TestReadToolAcceptsUnicodeNearScanPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	content := strings.Repeat("x", 8180) + "◆◇▲▼◀▶●○" + strings.Repeat("y", 200)
	if err := os.WriteFile(filepath.Join(tmpDir, "entities.py"), []byte(content), 0o644); err != nil {
		testutil.FailErr(t, "seed file", err)
	}
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "entities.py", "limit": 5}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read unicode near scan prefix", err)
	if !strings.Contains(out, "xxxxx") {
		t.Fatalf("read output = %q", out)
	}
}

func TestGrepTool(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "findme.txt"), []byte("line1\nneedle here\nline3"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "needle"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "tool.Run failed", err)
	if !strings.Contains(out, "needle") {
		t.Fatalf("grep = %q", out)
	}
	matches := nativefixture.GrepMatches(t, out)
	if len(matches) != 1 || matches[0]["line"].(float64) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestGrepToolProductionSandbox(t *testing.T) {
	sandboxCfg, err := sandbox.LoadConfig()
	testutil.FailErr(t, "load sandbox config", err)
	if !sandboxCfg.RejectSymlinkEscape {
		t.Fatal("expected production reject_symlink_escape=true")
	}
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	boundary := sandbox.NewBoundary(sandboxCfg, profiles)

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "findme.txt"), []byte("needle in production sandbox\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &GrepTool{Boundary: boundary}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "needle"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "tool.Run failed", err)
	if out == "null" || !strings.Contains(out, "needle") {
		t.Fatalf("grep with production sandbox = %q", out)
	}
}

func TestGrepToolMissingPattern(t *testing.T) {
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "."}, nativefixture.Context(t.TempDir()))
	if err == nil {
		t.Fatal("expected missing pattern error")
	}
}

func TestReadToolRejectsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o755); err != nil {
		testutil.FailErr(t, "os.Mkdir failed", err)
	}
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "."}, nativefixture.Context(tmpDir))
	if err == nil {
		t.Fatal("expected read to reject directory path")
	}
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "READ_IS_DIRECTORY" {
		t.Fatalf("err = %q want READ_IS_DIRECTORY", err)
	}
}

func TestReadToolRejectsOversizeFile(t *testing.T) {
	tmpDir := t.TempDir()
	f, err := os.Create(filepath.Join(tmpDir, "huge.log"))
	if err != nil {
		testutil.FailErr(t, "os.Create failed", err)
	}
	// Sparse: apparent size over the cap without writing the bytes.
	if err := f.Truncate(readcaps.MaxFileBytes + 1); err != nil {
		testutil.FailErr(t, "truncate failed", err)
	}
	if err := f.Close(); err != nil {
		testutil.FailErr(t, "close failed", err)
	}
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	_, err = tool.Run(context.Background(), map[string]any{"path": "huge.log"}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) {
		t.Fatalf("err = %v want ToolReject", err)
	}
	// A sparse file is all NUL, so READ_BINARY_DENIED here would mean the size
	// guard ran after the read — the allocation must be refused first.
	if reject.Code != "READ_FILE_TOO_LARGE" {
		t.Fatalf("code = %q want READ_FILE_TOO_LARGE", reject.Code)
	}
}

func TestReadToolFuzzyMiss(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "hello_world.txt"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "hello.txt"}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "READ_PATH_NOT_FOUND" {
		t.Fatalf("err = %v want READ_PATH_NOT_FOUND", err)
	}
	suggestions, _ := reject.Data["suggestions"].([]string)
	if len(suggestions) == 0 || suggestions[0] != "hello_world.txt" {
		t.Fatalf("suggestions = %v", reject.Data["suggestions"])
	}
}

func TestReadToolMissingPath(t *testing.T) {
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{}, nativefixture.Context(t.TempDir()))
	if err == nil {
		t.Fatal("expected missing path error")
	}
}

func TestReadToolOffsetBeyondEOF(t *testing.T) {
	tmpDir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("line\n")
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "short.txt"), []byte(b.String()), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":   "short.txt",
		"offset": float64(100),
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "READ_OFFSET_BEYOND_EOF" {
		t.Fatalf("err = %v want READ_OFFSET_BEYOND_EOF", err)
	}
}

func TestReadToolAutoOutlineLargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	// Blank lines cross the size boundary without adding syntax nodes.
	content := strings.Repeat("\n", readcaps.AutoOutlineThreshold+50) + "class Target:\n    pass\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "big.py"), []byte(content), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "big.py"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.Mode != "outline" {
		t.Fatalf("mode = %q want outline", resp.Mode)
	}
	if len(resp.Symbols) == 0 {
		t.Fatalf("expected symbols in outline")
	}
	if !strings.Contains(resp.TruncationBanner, "OUTLINE:") {
		t.Fatalf("banner = %q want OUTLINE prefix", resp.TruncationBanner)
	}
	if strings.Contains(resp.TruncationBanner, "TRUNCATED:") {
		t.Fatalf("outline banner must not use TRUNCATED prefix: %q", resp.TruncationBanner)
	}
	if !strings.Contains(resp.TruncationBanner, "large file") {
		t.Fatalf("banner = %q want large-file wording", resp.TruncationBanner)
	}
	if !strings.Contains(resp.TruncationBanner, "summarize") {
		t.Fatalf("banner = %q want summarize affordance", resp.TruncationBanner)
	}
}

func TestReadToolOutlineParseHealth(t *testing.T) {
	tmpDir := t.TempDir()
	clean := "function update() {\n  move();\n}\n"
	broken := "function update() {\n  move();\n\nfunction next() {}\n"
	testutil.FailErr(t, "write clean", os.WriteFile(filepath.Join(tmpDir, "clean.js"), []byte(clean), 0o644))
	testutil.FailErr(t, "write broken", os.WriteFile(filepath.Join(tmpDir, "broken.js"), []byte(broken), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}

	out, err := tool.Run(context.Background(), map[string]any{"path": "clean.js", "mode": "outline"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read clean", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode clean", json.Unmarshal([]byte(out), &resp))
	if resp.Parses == nil || !*resp.Parses {
		t.Fatalf("clean parses = %v want true", resp.Parses)
	}
	if strings.Contains(resp.TruncationBanner, "Parse:") {
		t.Fatalf("clean banner must not flag parse errors: %q", resp.TruncationBanner)
	}
	if resp.SurveyRecommended || strings.Contains(resp.TruncationBanner, "large file") ||
		strings.Contains(resp.TruncationBanner, "summarize") {
		t.Fatalf("short explicit outline must remain a short-file response: %+v", resp)
	}

	out, err = tool.Run(context.Background(), map[string]any{"path": "broken.js", "mode": "outline"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read broken", err)
	resp = ReadResponse{}
	testutil.FailErr(t, "decode broken", json.Unmarshal([]byte(out), &resp))
	if resp.Parses == nil || *resp.Parses {
		t.Fatalf("broken parses = %v want false", resp.Parses)
	}
	if len(resp.Errors) == 0 {
		t.Fatal("broken errors[] empty")
	}
	if !strings.Contains(resp.TruncationBanner, "Parse:") {
		t.Fatalf("broken banner = %q want PARSE flag", resp.TruncationBanner)
	}
}

func TestReadToolAutoOutlineWeakFileDiagnostics(t *testing.T) {
	tmpDir := t.TempDir()
	body := strings.Repeat("plain\n", readcaps.AutoOutlineThreshold+50)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "notes.txt"), []byte(body), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "notes.txt"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.OutlineSource != "regex" {
		t.Fatalf("outline_source = %q want regex", resp.OutlineSource)
	}
	if resp.Diagnostics == nil {
		t.Fatal("diagnostics = nil want a skip reason for the weak file")
	}
	skip := resp.Diagnostics.SkipReasons
	if skip.NoGrammar != 1 {
		t.Fatalf("diagnostics = %+v want no_grammar=1", resp.Diagnostics)
	}
	if !strings.Contains(resp.TruncationBanner, "no defs in symbols") {
		t.Fatalf("banner = %q", resp.TruncationBanner)
	}
	if !strings.Contains(resp.TruncationBanner, "summarize") {
		t.Fatalf("banner = %q want summarize affordance", resp.TruncationBanner)
	}
	if strings.Contains(resp.TruncationBanner, "TRUNCATED:") {
		t.Fatalf("outline banner must not use TRUNCATED prefix: %q", resp.TruncationBanner)
	}
}

func TestReadToolBatchRanges(t *testing.T) {
	tmpDir := t.TempDir()
	content := "a\nb\nc\nd\ne\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "slice.txt"), []byte(content), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path": "slice.txt",
		"ranges": []any{
			map[string]any{"offset": 1.0, "limit": 2.0},
			map[string]any{"offset": 4.0, "limit": 2.0},
		},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read batch", err)
	if !strings.Contains(out, `     1: a`) || !strings.Contains(out, `     4: d`) {
		t.Fatalf("out = %q", out)
	}
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.Mode != "ranges" || len(resp.Ranges) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestReadToolSymbolSingleMatch(t *testing.T) {
	tmpDir := t.TempDir()
	src := "package main\n\nfunc FooHandler() {\n\tprintln(\"x\")\n}\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "handler.go"), []byte(src), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":   "handler.go",
		"symbol": "FooHandler",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read symbol", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.Mode != "symbol" || resp.Count != 1 || resp.StartLine != 3 {
		t.Fatalf("resp = %+v", resp)
	}
	if !strings.Contains(resp.Content, "     3: func FooHandler") {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestReadToolSymbolNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "handler.go"), []byte("package main\n"), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":   "handler.go",
		"symbol": "Missing",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read symbol", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.Count != 0 || resp.Note == "" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestReadToolSymbolAmbiguous(t *testing.T) {
	tmpDir := t.TempDir()
	src := "package main\n\nfunc Close() {}\n\ntype Close struct{}\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "dup.go"), []byte(src), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":   "dup.go",
		"symbol": "Close",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read symbol", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.Count != 2 || len(resp.Candidates) != 2 || resp.Note == "" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestReadToolSymbolUnsupported(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "plain.zzzzz"), []byte("hello"), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":   "plain.zzzzz",
		"symbol": "hello",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read symbol", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.Count != 0 || !strings.Contains(resp.Note, "no grammar") {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestReadToolSymbolRejectsOffsetConflict(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "handler.go"), []byte("package main\n"), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path":   "handler.go",
		"symbol": "main",
		"offset": 1,
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if err == nil || !errors.As(err, &reject) || reject.Code != "READ_ARGS_CONFLICT" {
		t.Fatalf("err = %v want READ_ARGS_CONFLICT", err)
	}
}

func TestReadToolPlainReadUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "plain.go"), []byte("package main\n"), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "plain.go"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.Mode != "content" || resp.Symbol != "" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestReadToolLogOutlineMode(t *testing.T) {
	tmpDir := t.TempDir()
	line := `{"level":"info","ts":"2024-01-15T10:00:00Z","msg":"started"}` + "\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "app.log"), []byte(line+line), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "app.log", "mode": "outline"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.OutlineKind != api.OutlineKindLogDigest {
		t.Fatalf("outline_kind = %q want log_digest", resp.OutlineKind)
	}
	if resp.LogDigest == nil || resp.LogDigest.Format != logoutline.FormatJSONLines {
		t.Fatalf("log_digest = %+v", resp.LogDigest)
	}
	if resp.OutlineSource != "log" {
		t.Fatalf("outline_source = %q want log", resp.OutlineSource)
	}
	if len(resp.Symbols) != 0 {
		t.Fatalf("symbols = %+v want empty", resp.Symbols)
	}
}

func TestReadToolAutoOutlineLargeLogDigest(t *testing.T) {
	tmpDir := t.TempDir()
	line := []byte(`{"level":"info","ts":"2024-01-15T10:00:00Z","msg":"event"}` + "\n")
	var body []byte
	for i := 0; i < readcaps.AutoOutlineThreshold+50; i++ {
		body = append(body, line...)
	}
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "big.log"), body, 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "big.log"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.Mode != "outline" {
		t.Fatalf("mode = %q want outline", resp.Mode)
	}
	if resp.OutlineKind != api.OutlineKindLogDigest {
		t.Fatalf("outline_kind = %q want log_digest", resp.OutlineKind)
	}
	if resp.LogDigest == nil {
		t.Fatal("log_digest missing on auto-outline log")
	}
}

func TestReadToolCodeOutlineNoLogDigest(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "main.go", "mode": "outline"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read", err)
	var resp ReadResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &resp))
	if resp.OutlineKind != api.OutlineKindSymbols {
		t.Fatalf("outline_kind = %q want symbols", resp.OutlineKind)
	}
	if resp.LogDigest != nil {
		t.Fatalf("log_digest = %+v want nil", resp.LogDigest)
	}
	if len(resp.Symbols) == 0 {
		t.Fatal("expected symbols for go outline")
	}
}
