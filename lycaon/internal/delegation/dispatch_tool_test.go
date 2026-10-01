package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDispatchNoDuplicateNextStep(t *testing.T) {
	sessMgr, store, _, mgr, regProj := newDelegationTestManager(t)
	reg := tools.NewDefaultRegistry()
	if err := RegisterDispatchTool(reg, mgr); err != nil {
		testutil.FailErr(t, "RegisterDispatchTool failed", err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	projectID := seedDelegationProject(t, regProj, dir)
	sess, err := sessMgr.CreateForProject(ctx, projectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "CreateForProject", err)
	leg := api.Leg{ID: "leg-1", Title: "implement", Prompt: "do work", Files: []string{"src/**"}}
	delegation := api.Delegation{ProjectID: projectID, WorkspacePath: dir, Task: "do work"}
	if _, err := store.Create(ctx, delegation, sess.ID, []api.Leg{leg}); err != nil {
		testutil.FailErr(t, "create session in store", err)
	}
	out, err := reg.Run(ctx, "delegate_dispatch", map[string]any{"leg_id": leg.ID}, tools.ToolContext{SessionID: sess.ID})
	testutil.FailErr(t, "reg.Run failed", err)
	if strings.Contains(out, ">>> NEXT:") {
		t.Fatalf("dispatch output must not duplicate next_step suffix: %q", out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if _, ok := payload["next_step"]; !ok {
		t.Fatalf("missing next_step in %q", out)
	}
	if payload["status"] != "enqueued" {
		t.Fatalf("status = %v want enqueued", payload["status"])
	}
	workerID, _ := payload["worker_id"].(string)
	if strings.TrimSpace(workerID) == "" {
		t.Fatalf("missing worker_id in %q", out)
	}
}

func TestDispatchRejectWhenNoDelegation(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	if err := RegisterDispatchTool(reg, &Manager{Store: NewMemoryStore()}); err != nil {
		testutil.FailErr(t, "RegisterDispatchTool failed", err)
	}
	_, err := reg.Run(context.Background(), "delegate_dispatch", map[string]any{"leg_id": "leg-1"}, tools.ToolContext{SessionID: "orphan-session"})
	if err == nil {
		t.Fatal("expected reject when session has no delegation")
	}
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "COORDINATOR_DELEGATE_DISPATCH_USE_TASK" {
		t.Fatalf("expected COORDINATOR_DELEGATE_DISPATCH_USE_TASK reject, got %v", err)
	}

	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatted := tools.FormatDecisionReject(reject.Code, reject.Data, guidance.NewStaticRejectFormatter(cfg))
	if formatted == nil {
		t.Fatal("expected formatted reject")
	}
	block := formatted.Error()
	if !strings.Contains(block, "Code: COORDINATOR_DELEGATE_DISPATCH_USE_TASK") {
		t.Fatalf("missing reject code in block: %q", block)
	}
	if !strings.Contains(block, "task(") {
		t.Fatalf("missing task() guidance in block: %q", block)
	}
}
