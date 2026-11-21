package turnguards

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBeforeToolChecksWorkerScopeInAskMode(t *testing.T) {
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join("..", "..", "..", "..", "schemas"))
	testutil.FailErr(t, "load policy schemas", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load effective policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorSessionPreInvoke)

	policy := policyfacts.New(stubProfiles{})
	policy.SetSources(nil, nil, stubFeedback{}, nil)
	policy.SetPipeline(pipeline)
	lookupErr := errors.New("workflow work lookup failed")
	for _, tc := range []struct {
		name    string
		mode    api.SessionMode
		tool    string
		args    map[string]any
		check   func(*testing.T, string, map[string]any) error
		wantErr error
		reject  bool
	}{
		{name: "mutation-capable worker", mode: api.SessionModeAsk, tool: "task",
			args: map[string]any{"agent_type": "implementer"}, reject: true},
		{name: "planned write scope", mode: api.SessionModeAsk, tool: "task",
			args: map[string]any{"workflow_work_id": "write-leg"}, reject: true},
		{name: "unavailable planned leg", mode: api.SessionModeAsk, tool: "task",
			args: map[string]any{"workflow_work_id": "unavailable"}, wantErr: lookupErr},
		{name: "read-only worker", mode: api.SessionModeAsk, tool: "task",
			args: map[string]any{"agent_type": "repo-researcher"}},
		{name: "Agent write worker", mode: api.SessionModeAgent, tool: "task",
			args: map[string]any{"agent_type": "implementer"}},
		{name: "write instruction", mode: api.SessionModeAsk, tool: "instruct_worker",
			check: func(t *testing.T, id string, args map[string]any) error {
				if id != "coordinator" || args["worker"] != "reviewer" {
					t.Fatalf("instruction context = %q, args = %v", id, args)
				}
				return tools.AskModeReject("instruct_worker")
			}, args: map[string]any{"worker": "reviewer"}, reject: true},
		{name: "read-only instruction", mode: api.SessionModeAsk, tool: "instruct_worker",
			check: func(*testing.T, string, map[string]any) error { return nil }},
		{name: "unrelated tool", mode: api.SessionModeAsk, tool: "wait"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := &api.Session{ID: "coordinator", Mode: tc.mode, Posture: api.SessionPostureBuild}
			svc := New(stubSessions{sess: sess}, policy, nil, nil, stubState{}, nil, nil, nil, nil, nil, nil, nil)
			svc.SetSurface(stubSurface{})
			svc.SetTaskIdentityResolver(workeradmission.TaskIdentityResolver{WorkflowWork: func(_ context.Context, _, id string) (spawn.WorkflowWork, bool, error) {
				if id == "unavailable" {
					return spawn.WorkflowWork{}, false, lookupErr
				}
				return spawn.WorkflowWork{AgentType: "repo-researcher", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}}, true, nil
			}})
			if tc.check != nil {
				svc.SetAskInstructionCheck(func(_ context.Context, id string, args map[string]any) error {
					return tc.check(t, id, args)
				})
			}

			_, skipped, err := svc.BeforeTool(t.Context(), sess, nil, "", tc.tool, tc.args)
			if tc.reject {
				refusal, ok := guidance.RefusalFromError(err)
				if skipped || !ok || refusal.Code() != tools.AskModeActiveCode {
					t.Fatalf("worker call skipped = %v, error = %v, want Ask refusal", skipped, err)
				}
			} else if skipped || !errors.Is(err, tc.wantErr) {
				t.Fatalf("worker call skipped = %v, error = %v, want %v", skipped, err, tc.wantErr)
			}
		})
	}
}
