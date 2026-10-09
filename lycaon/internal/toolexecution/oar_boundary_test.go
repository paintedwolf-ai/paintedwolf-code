package toolexecution_test

import (
	"github.com/lycaon/lycaon/internal/toolfeedback"

	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestProf10HandlerRulePreventsActualToolExecution(t *testing.T) {
	if err := anchorcatalog.InstallBundled(); err != nil {
		t.Fatalf("install anchor catalogue: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "arbitrary-file.yaml"), []byte("oar: '1.0'\nid: STOP\nkind: policy\nanchor: tool.handler\nrequires:\n  profiles: [session, tool]\nwhen: principal == \"owner\" && session_posture == \"build\" && permission_profile == \"implement\"\neffect: block\n"), 0o600); err != nil {
		t.Fatalf("write policy fixture: %v", err)
	}
	loader, err := oar.NewLoader("")
	if err != nil {
		t.Fatalf("create policy loader: %v", err)
	}
	rules, err := loader.LoadDir(extpacks.OnDisk(dir))
	if err != nil {
		t.Fatalf("load handler policy: %v", err)
	}
	pipeline := oar.NewGuardPipeline(rules, loader, nil)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	pipeline.EnableAnchor(oar.AnchorToolHandler)
	calls := 0
	registry := tools.NewDefaultRegistry()
	if err := registry.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		calls++
		return "unreviewed output", errors.New("handler failed")
	}); err != nil {
		t.Fatalf("register tool handler: %v", err)
	}
	executor := toolexecution.NewExecutor(stubPolicy{decision: &platform.PolicyDecision{Allowed: true}}, registry, "implement")
	executor.Rejections.SetBlockPlane(&toolfeedback.BlockPlane{Pipeline: pipeline})
	ctx := curationctx.WithSession(t.Context(), curationctx.Session{SessionID: "handler-boundary", OwnerPersonID: "owner", Posture: "build"})
	output, err := executor.Invoke(ctx, "read", map[string]any{"path": "foo.go"}, tools.ToolContext{SessionID: "handler-boundary", Agent: "implement"})
	if err == nil || output != "" || calls != 0 {
		t.Fatalf("[OAR-PROF-10] output=%q err=%v calls=%d", output, err, calls)
	}
}
