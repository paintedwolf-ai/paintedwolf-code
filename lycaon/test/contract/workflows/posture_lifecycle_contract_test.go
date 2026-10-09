package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPlanWorkflowPostureLifecycleContract(t *testing.T) {
	ctx, mgr, sessStore, blueprintMgr := newBundledPlanWorkflowManager(t)

	run, err := mgr.Starts.StartHuman(ctx, "sess-posture", api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
		Request:    "test request",
		Parameters: map[string]string{"research_depth": "none"},
	})
	contractcheck.FailErr(t, "mgr.StartHuman failed", err)
	if run.CurrentPhase != "expand" {
		t.Fatalf("depth-none start phase = %q want expand", run.CurrentPhase)
	}
	seed, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	contractcheck.FailErr(t, "blueprintMgr.Get before seed", err)
	bp, err := blueprintMgr.Store.UpdateContent(ctx, run.ProjectID, run.BlueprintPath, conditions.TestPlanContentWithTasks, blueprint.ContentDigest(seed.Content))
	if err != nil {
		contractcheck.FailErr(t, "seed blueprint content", err)
	}
	assertSessionPosture(t, ctx, sessStore, "sess-posture", api.SessionPostureSpec)

	run, err = mgr.Advance(ctx, run.ID)
	if err != nil {
		t.Fatalf("advance expand: %v", err)
	}
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}
	assertSessionPosture(t, ctx, sessStore, "sess-posture", api.SessionPostureSpec)

	sess, err := sessStore.Get(ctx, "sess-posture")
	contractcheck.FailErr(t, "store.Get failed", err)
	run, err = mgr.Approvals.ApprovePlan(
		ctx, run.ProjectID, run.BlueprintPath, sess.WorkspacePath,
		run.ID, run.Revision, workflowdef.HashBlueprintContent(bp.Content),
	)
	contractcheck.FailErr(t, "ApprovePlan", err)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}
	assertSessionPosture(t, ctx, sessStore, "sess-posture", api.SessionPostureBuild)
}

func TestPostureRuleBehaviorMatrix(t *testing.T) {
	postures, err := profiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	packs, err := rules.LoadBundledRules()
	contractcheck.FailErr(t, "rules.LoadBundledRules failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		contractcheck.FailErr(t, "register rule conditions", err)
	}
	engine, err := rules.NewPostureRuleEngine(postures, packs, reg)
	contractcheck.FailErr(t, "rules.NewPostureRuleEngine failed", err)

	cases := []struct {
		name     string
		posture  api.SessionPosture
		tool     string
		args     map[string]any
		allowed  []string
		wantDeny bool
		wantCode string
	}{
		{"spec/delegate", api.SessionPostureSpec, "delegate_dispatch", nil, nil, true, "SPEC_POSTURE_DELEGATION_FORBIDDEN"},
		{"spec/state", api.SessionPostureSpec, "state_create", nil, nil, true, "SPEC_POSTURE_STATE_FORBIDDEN"},
		{"build/delegate", api.SessionPostureBuild, "delegate_dispatch", nil, nil, false, ""},
		{"build/disallowed-agent", api.SessionPostureBuild, "task", map[string]any{"agent_type": "sentinel"}, []string{"implementer"}, true, "DISALLOWED_AGENT"},
		{"vet/delegate", api.SessionPostureVet, "delegate_dispatch", nil, nil, true, "VET_POSTURE_DELEGATION_FORBIDDEN"},
		// A vet turn resolves to an observe_ surface that offers no write.
		{"vet/write", api.SessionPostureVet, "write", nil, nil, false, ""},
		{"spec/write", api.SessionPostureSpec, "write", nil, nil, false, ""},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := engine.Evaluate(ctx, rules.EvalContext{EvalContext: conditions.EvalContext{SessionPosture: tc.posture, ToolName: tc.tool, ToolArgs: tc.args, AllowedAgents: tc.allowed}})
			contractcheck.FailErr(t, "engine.Evaluate failed", err)
			if tc.wantDeny {
				if out.Allowed {
					t.Fatal("expected deny")
				}
				if out.Code != tc.wantCode {
					t.Fatalf("code = %q want %q", out.Code, tc.wantCode)
				}
				return
			}
			if !out.Allowed {
				t.Fatalf("expected allow, got deny %q", out.Code)
			}
		})
	}
}

// newBundledPlanWorkflowManager builds the plan workflow stack over a
// disposable store and returns the host owner's caller context, since plan
// approval is an authenticated decision.
func newBundledPlanWorkflowManager(t *testing.T) (context.Context, *workflow.RunManager, session.Store, *blueprint.Manager) {
	t.Helper()
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "posture-lifecycle.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)

	testdbseed.InsertSessionWithRoot(t, sqlDB, "sess-posture", testdbseed.DefaultProjectID, projectDir)

	sessStore := store.NewSQL(sqlDB)
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "workflow.RegistryFromDirs failed", err)
	blueprintStore := blueprint.NewFileStoreForTest(projectDir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	runStore := workflowpersistence.New(sqlDB)
	// The seal is a precondition of the grant, so the run store carries the same
	// recorder the sidecar wires.
	runStore.Transactions.SetAuthzRecorder(authzcontext.SQLRecorder(sqlDB))
	mgr := workflow.NewManager(runStore, sessStore, manifestRegistry, nil)
	blueprintMgr.AfterRetarget = mgr.Blueprints.RebindBlueprintPath
	mgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	mgr.Blueprints.Getter = blueprintMgr
	mgr.Presentation.BlueprintGetter = blueprintMgr
	mgr.Approvals.Getter = blueprintMgr
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(ctx context.Context, path string) (*api.Blueprint, error) {
			return blueprintMgr.Get(ctx, testdbseed.DefaultProjectID, path)
		},
		BlueprintContent: func(_ context.Context, projectDir, relPath string) (string, error) {
			return workflowblueprintfiles.ReadBlueprintFile(projectDir, relPath)
		},
	})
	contractcheck.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	mgr.SetConditionRegistry(condReg)
	return ctx, mgr, sessStore, blueprintMgr
}

func assertSessionPosture(t *testing.T, ctx context.Context, store session.Store, id string, want api.SessionPosture) {
	t.Helper()
	sess, err := store.Get(ctx, id)
	contractcheck.FailErr(t, "store.Get failed", err)
	if sess.Posture != want {
		t.Fatalf("session %s posture = %q want %q", id, sess.Posture, want)
	}
}
