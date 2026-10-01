package survey

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

func findOutJSON(t *testing.T, out string) string {
	t.Helper()
	resp := parseFindResponse(t, out)
	raw, err := surveyjson.Marshal(resp)
	testutil.FailErr(t, "marshal find resp", err)
	return string(raw)
}

func writeManyFindFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file_%04d.txt", i)
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
	}
}

func TestFindAltitudeOverflowZoomedOut(t *testing.T) {
	tmpDir := t.TempDir()
	overflow := safecmd.FindMaxResults + 50
	writeManyFindFiles(t, tmpDir, overflow)
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"type": "file"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find overflow", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 0 {
		t.Fatalf("results = %d want omitted on overflow", len(resp.Results))
	}
	if len(resp.Distribution) == 0 {
		t.Fatal("expected distribution on overflow")
	}
	if resp.Total != overflow {
		t.Fatalf("total = %d want %d", resp.Total, overflow)
	}
	if resp.TotalResults != overflow {
		t.Fatalf("total_results = %d want %d", resp.TotalResults, overflow)
	}
	if resp.View != surveyViewDigest {
		t.Fatalf("view = %q want digest", resp.View)
	}
	if resp.Selected != 0 {
		t.Fatalf("selected = %d want 0 (Tier 1)", resp.Selected)
	}
	if !strings.Contains(resp.Note, "Specifics:") {
		t.Fatalf("note = %q want specifics affordance", resp.Note)
	}
	if !resp.Truncated || resp.NextOffset == nil || *resp.NextOffset != safecmd.FindMaxResults {
		t.Fatalf("pagination = truncated=%v next=%v", resp.Truncated, resp.NextOffset)
	}
	if !evidence.FindEvidenceSurvey(map[string]any{"type": "file"}, findOutJSON(t, out)) {
		t.Fatal("overflow find must be survey-grade")
	}
}

func TestFindAltitudeOffsetLiteral(t *testing.T) {
	tmpDir := t.TempDir()
	writeManyFindFiles(t, tmpDir, safecmd.FindMaxResults+50)
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"type":        "file",
		"offset":      float64(safecmd.FindMaxResults),
		"max_results": float64(50),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find offset page", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) == 0 {
		t.Fatal("expected literal results on explicit offset")
	}
	if len(resp.Highlights) != 0 {
		t.Fatal("offset page must not include curated highlights")
	}
	if evidence.FindEvidenceSurvey(map[string]any{"type": "file", "offset": safecmd.FindMaxResults}, findOutJSON(t, out)) {
		t.Fatal("explicit offset find must not be survey-grade")
	}
}

func TestFindAltitudeExpressedScopeLiteral(t *testing.T) {
	tmpDir := t.TempDir()
	writeManyFindFiles(t, tmpDir, safecmd.FindMaxResults+50)
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"type":        "file",
		"max_results": float64(100),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find scoped", err)
	resp := parseFindResponse(t, out)
	if len(resp.Results) != 100 {
		t.Fatalf("results = %d want 100 literal page", len(resp.Results))
	}
	if resp.View != "" {
		t.Fatalf("view = %q want empty on literal", resp.View)
	}
	if len(resp.Distribution) != 0 || resp.Total != 0 {
		t.Fatalf("distribution/total should be absent on literal: dist=%d total=%d", len(resp.Distribution), resp.Total)
	}
}

func TestFindAltitudeLiteralSacredGolden(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "only.txt"), []byte("x"), 0o644))
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	args := map[string]any{"type": "file"}
	out, err := tool.Run(context.Background(), args, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find", err)
	got := findOutJSON(t, out)

	size := int64(1)
	wantResp := findResponse{
		Results:      []findResult{{Path: "only.txt", Type: "file", Size: &size}},
		Offset:       0,
		TotalResults: 1,
		MaxDepth:     safecmd.FindListingDepth,
		MaxResults:   safecmd.FindMaxResults,
	}
	wantRaw, err := surveyjson.Marshal(wantResp)
	testutil.FailErr(t, "marshal want", err)
	if got != string(wantRaw) {
		t.Fatalf("narrow find JSON drift:\ngot  %s\nwant %s", got, string(wantRaw))
	}
	if evidence.FindEvidenceSurvey(args, got) {
		t.Fatal("narrow find must not be survey-grade")
	}
}

func TestFindAltitudeClassifier(t *testing.T) {
	if findIsUnbounded(map[string]any{"offset": 0}, true) {
		t.Fatal("offset should force literal")
	}
	if findIsUnbounded(map[string]any{"max_results": 100}, true) {
		t.Fatal("explicit max_results should force literal")
	}
	if findIsUnbounded(map[string]any{"max_results": safecmd.FindMaxResults}, true) {
		t.Fatal("explicit max_results at host cap should force literal")
	}
	if findIsUnbounded(map[string]any{"path": "docs"}, true) {
		t.Fatal("concrete subpath should force literal")
	}
	if findIsUnbounded(map[string]any{"name_glob": "fixture_*.md"}, true) {
		t.Fatal("name_glob should force literal")
	}
	if findIsUnbounded(map[string]any{"path": ".", "name_glob": "*.go"}, true) {
		t.Fatal("name_glob with root path should force literal")
	}
	if !findIsUnbounded(map[string]any{}, true) {
		t.Fatal("host-cap truncation without scope should be unbounded")
	}
	if !findIsUnbounded(map[string]any{"path": ".", "type": "file"}, true) {
		t.Fatal("root path + type without name_glob should stay unbounded on overflow")
	}
	if findIsUnbounded(map[string]any{}, false) {
		t.Fatal("non-truncated walk should not zoom")
	}
}

func TestFindAltitudeSubpathOverflowLiteral(t *testing.T) {
	tmpDir := t.TempDir()
	sub := filepath.Join(tmpDir, "plans")
	testutil.FailErr(t, "mkdir", os.Mkdir(sub, 0o755))
	writeManyFindFiles(t, sub, safecmd.FindMaxResults+50)
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path": "plans",
		"type": "file",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find subpath overflow", err)
	resp := parseFindResponse(t, out)
	if resp.View == surveyViewDigest {
		t.Fatalf("view = %q want literal on concrete subpath overflow", resp.View)
	}
	if len(resp.Results) != safecmd.FindMaxResults {
		t.Fatalf("results = %d want %d literal page", len(resp.Results), safecmd.FindMaxResults)
	}
	if len(resp.Distribution) != 0 {
		t.Fatalf("distribution should be absent on literal subpath: %d", len(resp.Distribution))
	}
	if !resp.Truncated || resp.NextOffset == nil || *resp.NextOffset != safecmd.FindMaxResults {
		t.Fatalf("pagination = truncated=%v next=%v", resp.Truncated, resp.NextOffset)
	}
	if evidence.FindEvidenceSurvey(map[string]any{"path": "plans", "type": "file"}, findOutJSON(t, out)) {
		t.Fatal("subpath find must not be survey-grade")
	}
}

func TestFindAltitudeNameGlobLiteral(t *testing.T) {
	tmpDir := t.TempDir()
	writeManyFindFiles(t, tmpDir, safecmd.FindMaxResults+50)
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("fixture_%d_note.md", i)
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, name), []byte("x"), 0o644))
	}
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path":      ".",
		"type":      "file",
		"name_glob": "fixture_*.md",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find name_glob", err)
	resp := parseFindResponse(t, out)
	if resp.View == surveyViewDigest {
		t.Fatalf("view = %q want literal with name_glob", resp.View)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %d want 3 name_glob hits", len(resp.Results))
	}
}

func TestFindAltitudeExplicitHostCapLiteralPage(t *testing.T) {
	tmpDir := t.TempDir()
	writeManyFindFiles(t, tmpDir, safecmd.FindMaxResults+50)
	tool := &FindTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"type":        "file",
		"max_results": float64(safecmd.FindMaxResults),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "find host-cap literal", err)
	resp := parseFindResponse(t, out)
	if resp.View != "" {
		t.Fatalf("view = %q want empty on literal", resp.View)
	}
	if len(resp.Results) != safecmd.FindMaxResults {
		t.Fatalf("results = %d want %d literal page", len(resp.Results), safecmd.FindMaxResults)
	}
	if len(resp.Highlights) != 0 {
		t.Fatal("explicit max_results must not enter digest highlights")
	}
	if !resp.Truncated || resp.NextOffset == nil || *resp.NextOffset != safecmd.FindMaxResults {
		t.Fatalf("pagination = truncated=%v next=%v", resp.Truncated, resp.NextOffset)
	}
	if evidence.FindEvidenceSurvey(map[string]any{"type": "file", "max_results": safecmd.FindMaxResults}, findOutJSON(t, out)) {
		t.Fatal("explicit max_results must not be survey-grade")
	}
}
