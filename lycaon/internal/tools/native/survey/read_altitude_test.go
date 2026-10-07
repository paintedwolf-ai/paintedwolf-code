package survey

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

func decodeReadOut(t *testing.T, out string) ReadResponse {
	t.Helper()
	var resp ReadResponse
	testutil.FailErr(t, "decode read out", json.Unmarshal([]byte(out), &resp))
	return resp
}

func readOutJSON(t *testing.T, out string) string {
	t.Helper()
	resp := decodeReadOut(t, out)
	raw, err := surveyjson.Marshal(resp)
	testutil.FailErr(t, "marshal read resp", err)
	return string(raw)
}

func testCtxSession(dir, sessionID string) tools.ToolContext {
	ctx := nativefixture.Context(dir)
	ctx.SessionID = sessionID
	return ctx
}

func writeLargeGoFile(t *testing.T, dir, name string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("package big\n\nfunc Marker() {}\n")
	for i := 0; i < readcaps.AutoOutlineThreshold+10; i++ {
		b.WriteString("// filler\n")
	}
	testutil.FailErr(t, "write large file", os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644))
}

func TestReadAltitudeDispatchUnboundedOutline(t *testing.T) {
	tmpDir := t.TempDir()
	writeLargeGoFile(t, tmpDir, "big.go")
	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Escalation: NewReadEscalationStore()}
	out, err := tool.Run(context.Background(), map[string]any{"path": "big.go"}, testCtxSession(tmpDir, "sess-1"))
	testutil.FailErr(t, "read", err)
	resp := decodeReadOut(t, out)
	if resp.Mode != "outline" {
		t.Fatalf("mode = %q want outline", resp.Mode)
	}
	if resp.Total <= 0 {
		t.Fatalf("total = %d want >0", resp.Total)
	}
	if resp.Selected != 0 {
		t.Fatalf("selected = %d want 0 without curator", resp.Selected)
	}
	if !strings.Contains(resp.Note, "Specifics:") {
		t.Fatalf("note = %q want specifics affordance", resp.Note)
	}
	if !strings.Contains(resp.Note, "summarize") {
		t.Fatalf("note = %q want summarize affordance for large-file outline", resp.Note)
	}
	if !strings.Contains(resp.TruncationBanner, "summarize") {
		t.Fatalf("truncation_banner = %q want summarize for how/what", resp.TruncationBanner)
	}
}

func TestReadAltitudeDispatchExpressedScopeLiteral(t *testing.T) {
	tmpDir := t.TempDir()
	writeLargeGoFile(t, tmpDir, "big.go")
	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Escalation: NewReadEscalationStore()}
	out, err := tool.Run(context.Background(), map[string]any{"path": "big.go", "offset": 1, "limit": 3}, testCtxSession(tmpDir, "sess-1"))
	testutil.FailErr(t, "read", err)
	resp := decodeReadOut(t, out)
	if resp.Mode != "content" {
		t.Fatalf("mode = %q want content", resp.Mode)
	}
	if resp.Selected != 0 || resp.Total != 0 {
		t.Fatalf("selected/total should be omitted for literal read: selected=%d total=%d", resp.Selected, resp.Total)
	}
	if !strings.Contains(resp.Content, "package big") {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestReadAltitudeLiteralSacredGolden(t *testing.T) {
	tmpDir := t.TempDir()
	content := "package main\n\nfunc main() {}\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(content), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t)}
	args := map[string]any{"path": "main.go", "offset": 1, "limit": 2}
	out, err := tool.Run(context.Background(), args, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "read", err)
	got := readOutJSON(t, out)

	next := 3
	wantResp := ReadResponse{
		Path:             "main.go",
		Mode:             "content",
		Content:          hostmarker.FormatNumberedLines([]string{"package main", ""}, 1),
		TotalLines:       3,
		Offset:           1,
		Limit:            2,
		EndLine:          2,
		Truncated:        true,
		NextOffset:       &next,
		TruncationBanner: toolkit.TruncationBanner("1 more lines; use offset=3 limit=2"),
	}
	wantRaw, err := surveyjson.Marshal(wantResp)
	testutil.FailErr(t, "marshal want", err)
	if got != string(wantRaw) {
		t.Fatalf("literal read JSON drift:\ngot  %s\nwant %s", got, string(wantRaw))
	}
	if evidence.ReadEvidenceSurvey(args, got) {
		t.Fatal("expressed-scope read must not be survey-grade")
	}
}

func TestReadAltitudeEscalationSecondStrikeLiteralFull(t *testing.T) {
	tmpDir := t.TempDir()
	writeLargeGoFile(t, tmpDir, "big.go")
	esc := NewReadEscalationStore()
	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Escalation: esc}
	ctx := testCtxSession(tmpDir, "sess-esc")

	out1, err := tool.Run(context.Background(), map[string]any{"path": "big.go"}, ctx)
	testutil.FailErr(t, "first read", err)
	first := decodeReadOut(t, out1)
	if first.Mode != "outline" {
		t.Fatalf("first mode = %q want outline", first.Mode)
	}

	out2, err := tool.Run(context.Background(), map[string]any{"path": "big.go"}, ctx)
	testutil.FailErr(t, "second read", err)
	second := decodeReadOut(t, out2)
	if second.Mode != "content" {
		t.Fatalf("second mode = %q want content", second.Mode)
	}
	if second.Truncated {
		t.Fatal("escalated full read must not truncate")
	}
	if second.Offset != 1 || second.Limit != second.TotalLines {
		t.Fatalf("second offset/limit = %d/%d total=%d want full file", second.Offset, second.Limit, second.TotalLines)
	}
	if evidence.ReadEvidenceSurvey(map[string]any{"path": "big.go"}, readOutJSON(t, out2)) {
		t.Fatal("escalated literal full read must not be survey-grade")
	}
}

func TestReadAltitudeExpressedScopeBypassesEscalation(t *testing.T) {
	tmpDir := t.TempDir()
	writeLargeGoFile(t, tmpDir, "big.go")
	esc := NewReadEscalationStore()
	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Escalation: esc}
	ctx := testCtxSession(tmpDir, "sess-bypass")

	_, err := tool.Run(context.Background(), map[string]any{"path": "big.go", "offset": 1, "limit": 1}, ctx)
	testutil.FailErr(t, "scoped read", err)
	if esc.Strike("sess-bypass", "big.go") != 0 {
		t.Fatalf("strike = %d want 0 after expressed-scope read", esc.Strike("sess-bypass", "big.go"))
	}

	out, err := tool.Run(context.Background(), map[string]any{"path": "big.go"}, ctx)
	testutil.FailErr(t, "bare read", err)
	resp := decodeReadOut(t, out)
	if resp.Mode != "outline" {
		t.Fatalf("mode = %q want outline on first unbounded after scoped read", resp.Mode)
	}
}

func TestReadHasExpressedScopeClassifier(t *testing.T) {
	large := readcaps.AutoOutlineThreshold + 1
	if !readHasExpressedScope("", map[string]any{"offset": 1}, large) {
		t.Fatal("offset should express scope")
	}
	if !readHasExpressedScope("", map[string]any{"limit": 5}, large) {
		t.Fatal("limit should express scope")
	}
	if !readHasExpressedScope("", map[string]any{"symbol": "main"}, large) {
		t.Fatal("symbol should express scope")
	}
	if !readHasExpressedScope("", map[string]any{}, readcaps.AutoOutlineThreshold) {
		t.Fatal("small bare file should express scope via threshold")
	}
	if readHasExpressedScope("", map[string]any{}, large) {
		t.Fatal("large bare file should not express scope")
	}
	if !readWantsOutline("", map[string]any{}, large) {
		t.Fatal("large bare file should be unbounded")
	}
}
