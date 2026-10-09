package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

type presenceRecorder struct {
	targets []agentpresence.Target
	kinds   []api.AgentActivityKind
	intents [][]agentpresence.Intent
}

func (r *presenceRecorder) Target(target agentpresence.Target, kind api.AgentActivityKind) {
	r.targets = append(r.targets, target)
	r.kinds = append(r.kinds, kind)
}
func (r *presenceRecorder) Intents(intents []agentpresence.Intent) {
	r.intents = append(r.intents, intents)
}
func (r *presenceRecorder) AwaitingApproval(string)         {}
func (r *presenceRecorder) Approved()                       {}
func (r *presenceRecorder) Landed([]agentpresence.Document) {}
func (r *presenceRecorder) Reserved([]agentpresence.Target) {}
func (r *presenceRecorder) Released([]agentpresence.Target) {}

func presenceCtx(t *testing.T, files map[string]string) (tools.ToolContext, *presenceRecorder) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(path, []byte(body), 0o644))
	}
	tctx := nativefixture.Context(dir)
	recorder := &presenceRecorder{}
	tctx.Effects.Presence = recorder
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	return tctx, recorder
}

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line\n")
	}
	return b.String()
}

func TestReadCapturesTheLinesItReturned(t *testing.T) {
	cases := []struct {
		name   string
		args   map[string]any
		extent api.AgentPresenceExtent
		spans  [][2]int
	}{
		{"whole file", map[string]any{"path": "a.go", "offset": 1, "limit": 50}, api.AgentPresenceExtentWholeFile, nil},
		{"page", map[string]any{"path": "a.go", "offset": 5, "limit": 3}, api.AgentPresenceExtentRange, [][2]int{{5, 7}}},
		{"ranges", map[string]any{"path": "a.go", "ranges": []any{map[string]any{"offset": 2, "limit": 2}, map[string]any{"offset": 10, "limit": 1}}}, api.AgentPresenceExtentRange, [][2]int{{2, 3}, {10, 10}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tctx, recorder := presenceCtx(t, map[string]string{"a.go": numbered(20)})
			_, err := (&surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), tc.args, tctx)
			testutil.FailErr(t, "read", err)
			if len(recorder.targets) != 1 || recorder.targets[0] != (agentpresence.Target{RootID: "r1", Path: "a.go"}) || recorder.kinds[0] != api.AgentActivityKindReading {
				t.Fatalf("targets = %+v kinds = %v", recorder.targets, recorder.kinds)
			}
			reads := tctx.Effects.Out.SourceReads
			if len(reads) != 1 || reads[0].Extent != tc.extent || len(reads[0].Spans) != len(tc.spans) {
				t.Fatalf("reads = %+v", reads)
			}
			for i, span := range tc.spans {
				if reads[0].Spans[i].StartLine != span[0] || reads[0].Spans[i].EndLine != span[1] {
					t.Fatalf("span %d = %+v, want %v", i, reads[0].Spans[i], span)
				}
			}
		})
	}
}

func TestOutlineReadReturnsNoText(t *testing.T) {
	tctx, _ := presenceCtx(t, map[string]string{"a.go": "package a\n\nfunc One() {}\n"})
	_, err := (&surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"path": "a.go", "mode": "outline"}, tctx)
	testutil.FailErr(t, "outline read", err)
	if len(tctx.Effects.Out.SourceReads) != 0 {
		t.Fatalf("outline captured %+v", tctx.Effects.Out.SourceReads)
	}
}

func TestGrepCapturesEveryOccurrenceInCharacters(t *testing.T) {
	tctx, recorder := presenceCtx(t, map[string]string{"pkg/a.go": "é needle and needle\nnothing\n", "pkg/b.go": "needle\n"})
	_, err := (&surveytools.GrepTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"pattern": "needle", "path": "pkg"}, tctx)
	testutil.FailErr(t, "grep", err)
	if len(recorder.targets) != 1 || recorder.targets[0].Path != "pkg" {
		t.Fatalf("scope target = %+v", recorder.targets)
	}
	reads := tctx.Effects.Out.SourceReads
	if len(reads) != 2 {
		t.Fatalf("reads = %+v", reads)
	}
	first := reads[0]
	if first.Path != "pkg/a.go" || first.Extent != api.AgentPresenceExtentMatches || len(first.Spans) != 2 || len(first.ItemSpans) != 1 || first.ItemSpans[0] != 2 {
		t.Fatalf("first read = %+v", first)
	}
	// "é" is one UTF-16 unit; the second occurrence starts after "é needle and ".
	if *first.Spans[0].StartCharacter != 2 || *first.Spans[0].EndCharacter != 8 || *first.Spans[1].StartCharacter != 13 {
		t.Fatalf("occurrence characters = %d-%d, %d", *first.Spans[0].StartCharacter, *first.Spans[0].EndCharacter, *first.Spans[1].StartCharacter)
	}
}

func TestTextIntentsNameTheLinesAWriteReplaces(t *testing.T) {
	tctx, recorder := presenceCtx(t, map[string]string{"a.go": "one\ntwo\nthree\nfour\n"})
	_, err := (&ReplaceLinesTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"path": "a.go", "start_line": 2, "end_line": 3, "new_content": "TWO\nTHREE\nextra"}, tctx)
	testutil.FailErr(t, "replace lines", err)
	if len(recorder.intents) != 1 {
		t.Fatalf("intents = %+v", recorder.intents)
	}
	intent := recorder.intents[0][0]
	if intent.Operation != api.AgentIntentOperationEdit || intent.Extent != api.AgentPresenceExtentRange || len(intent.Spans) != 1 || intent.Spans[0].StartLine != 2 || intent.Spans[0].EndLine != 3 {
		t.Fatalf("intent = %+v", intent)
	}
}

func TestWorkerBranchWritesReportNoIntent(t *testing.T) {
	tctx, recorder := presenceCtx(t, map[string]string{"a.go": "one\n"})
	resolved := projectpaths.Resolved{Root: tctx.Source.Roots[0], ScopeRel: "a.go", DisplayPath: "a.go"}
	tctx.Source.WorkerBranchRoot = t.TempDir()
	reportTextIntent(tctx, resolved, sourceview.Text{Content: "one\n"}, true, "two\n")
	if len(recorder.intents) != 0 {
		t.Fatalf("worker write reported %+v", recorder.intents)
	}
}
