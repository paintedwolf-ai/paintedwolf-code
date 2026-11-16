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

func TestTextIntentsNameTheLinesAWriteReplaces(t *testing.T) {
	tctx, recorder := presenceCtx(t, map[string]string{"a.go": "one\ntwo\nthree\nfour\n"})
	_, err := (&WriteTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"path": "a.go", "content": "one\nTWO\nTHREE\nextra\nfour\n"}, tctx)
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
