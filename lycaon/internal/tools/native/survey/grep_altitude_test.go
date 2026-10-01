package survey

import (
	"context"
	"encoding/json"
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

func decodeGrepOut(t *testing.T, out string) grepResponse {
	t.Helper()
	var resp grepResponse
	testutil.FailErr(t, "decode grep out", json.Unmarshal([]byte(out), &resp))
	return resp
}

func grepOutJSON(t *testing.T, out string) string {
	t.Helper()
	resp := decodeGrepOut(t, out)
	raw, err := surveyjson.Marshal(resp)
	testutil.FailErr(t, "marshal grep resp", err)
	return string(raw)
}

func writeGrepHits(t *testing.T, dir string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("needle\n")
	}
	testutil.FailErr(t, "write hits", os.WriteFile(filepath.Join(dir, "many.txt"), []byte(b.String()), 0o644))
}

func TestGrepAltitudeOverflowZoomedOut(t *testing.T) {
	tmpDir := t.TempDir()
	overflow := safecmd.GrepMaxMatches + 50
	writeGrepHits(t, tmpDir, overflow)
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"pattern": "needle"}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep overflow", err)
	resp := decodeGrepOut(t, out)
	if len(resp.Matches) != 0 {
		t.Fatalf("matches = %d want omitted on overflow", len(resp.Matches))
	}
	if len(resp.Distribution) == 0 {
		t.Fatal("expected distribution on overflow")
	}
	if resp.Total != safecmd.GrepMaxMatches {
		t.Fatalf("total = %d want %d (host cap)", resp.Total, safecmd.GrepMaxMatches)
	}
	if resp.View != surveyViewDigest {
		t.Fatalf("view = %q want digest", resp.View)
	}
	if resp.Selected != 0 {
		t.Fatalf("selected = %d want 0 without curator", resp.Selected)
	}
	if !strings.Contains(resp.Note, "Specifics:") {
		t.Fatalf("note = %q want specifics affordance", resp.Note)
	}
	if !resp.Truncated || resp.NextOffset == nil || *resp.NextOffset != safecmd.GrepMaxMatches {
		t.Fatalf("pagination = truncated=%v next=%v", resp.Truncated, resp.NextOffset)
	}
	if !evidence.GrepEvidenceSurvey(map[string]any{"pattern": "needle"}, grepOutJSON(t, out)) {
		t.Fatal("overflow grep must be survey-grade")
	}
}

func TestGrepAltitudeOverflowOffsetLiteral(t *testing.T) {
	tmpDir := t.TempDir()
	writeGrepHits(t, tmpDir, safecmd.GrepMaxMatches+50)
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern": "needle",
		"offset":  float64(safecmd.GrepMaxMatches),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep offset page", err)
	resp := decodeGrepOut(t, out)
	if len(resp.Matches) == 0 {
		t.Fatal("expected literal matches on explicit offset")
	}
	if len(resp.Highlights) != 0 {
		t.Fatal("offset page must not include curated highlights")
	}
	if evidence.GrepEvidenceSurvey(map[string]any{"pattern": "needle", "offset": safecmd.GrepMaxMatches}, grepOutJSON(t, out)) {
		t.Fatal("explicit offset grep must not be survey-grade")
	}
}

func TestGrepAltitudeExpressedScopeLiteral(t *testing.T) {
	tmpDir := t.TempDir()
	writeGrepHits(t, tmpDir, safecmd.GrepMaxMatches+50)
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":     "needle",
		"max_matches": float64(50),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep scoped", err)
	resp := decodeGrepOut(t, out)
	if len(resp.Matches) != 50 {
		t.Fatalf("matches = %d want 50 literal page", len(resp.Matches))
	}
	if resp.View != "" {
		t.Fatalf("view = %q want empty on literal", resp.View)
	}
	if len(resp.Highlights) != 0 || resp.Total != 0 {
		t.Fatalf("highlights/total should be absent: highlights=%d total=%d", len(resp.Highlights), resp.Total)
	}
	if evidence.GrepEvidenceSurvey(map[string]any{"pattern": "needle", "max_matches": 50}, grepOutJSON(t, out)) {
		t.Fatal("expressed max_matches below host cap must not be survey-grade")
	}
}

func TestGrepAltitudeLiteralSacredGolden(t *testing.T) {
	tmpDir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("alpha\nbeta\n"), 0o644))
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	args := map[string]any{"pattern": "alpha"}
	out, err := tool.Run(context.Background(), args, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep", err)
	got := grepOutJSON(t, out)

	wantResp := grepResponse{
		FilesSearched: 1,
		Matches:       []grepMatch{{Path: "a.txt", Line: 1, Content: "alpha", Match: "alpha"}},
		Offset:        0,
		MaxMatches:    safecmd.GrepMaxMatches,
	}
	wantRaw, err := surveyjson.Marshal(wantResp)
	testutil.FailErr(t, "marshal want", err)
	if got != string(wantRaw) {
		t.Fatalf("narrow grep JSON drift:\ngot  %s\nwant %s", got, string(wantRaw))
	}
	if evidence.GrepEvidenceSurvey(args, got) {
		t.Fatal("narrow grep must not be survey-grade")
	}
}

func TestGrepAltitudeClassifier(t *testing.T) {
	if grepIsUnbounded(map[string]any{"offset": 0}, true) {
		t.Fatal("offset should force literal")
	}
	if grepIsUnbounded(map[string]any{"max_matches": 50}, true) {
		t.Fatal("explicit max_matches should force literal")
	}
	if grepIsUnbounded(map[string]any{"max_matches": safecmd.GrepMaxMatches}, true) {
		t.Fatal("explicit max_matches at host cap should force literal")
	}
	if !grepIsUnbounded(map[string]any{}, true) {
		t.Fatal("host-cap truncation without scope should be unbounded")
	}
	if grepIsUnbounded(map[string]any{}, false) {
		t.Fatal("non-truncated search should not zoom")
	}
}

func TestGrepAltitudeExplicitHostCapLiteralPage(t *testing.T) {
	tmpDir := t.TempDir()
	writeGrepHits(t, tmpDir, safecmd.GrepMaxMatches+50)
	tool := &GrepTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"pattern":     "needle",
		"max_matches": float64(safecmd.GrepMaxMatches),
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "grep host-cap literal", err)
	resp := decodeGrepOut(t, out)
	if resp.View != "" {
		t.Fatalf("view = %q want empty on literal", resp.View)
	}
	if len(resp.Matches) != safecmd.GrepMaxMatches {
		t.Fatalf("matches = %d want %d literal page", len(resp.Matches), safecmd.GrepMaxMatches)
	}
	if len(resp.Highlights) != 0 {
		t.Fatal("explicit max_matches must not enter digest highlights")
	}
	if !resp.Truncated || resp.NextOffset == nil || *resp.NextOffset != safecmd.GrepMaxMatches {
		t.Fatalf("pagination = truncated=%v next=%v", resp.Truncated, resp.NextOffset)
	}
	if evidence.GrepEvidenceSurvey(map[string]any{"pattern": "needle", "max_matches": safecmd.GrepMaxMatches}, grepOutJSON(t, out)) {
		t.Fatal("explicit max_matches must not be survey-grade")
	}
}
