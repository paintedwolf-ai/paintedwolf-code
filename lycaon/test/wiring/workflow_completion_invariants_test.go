package wiring

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowCatalogCompletionAuthorityInvariant(t *testing.T) {
	h := BuildForTest(t, WithoutCoordinatorLoop())
	registry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load shipped workflows", err)
	manifests := registry.All()
	keys := make([]string, 0, len(manifests))
	for key := range manifests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	dir := h.ProjectDir(t, "completion-contracts")
	testdbseed.InsertProjectRoot(t, h.DB, testdbseed.DefaultProjectID, dir)
	checked, blocked, ordinary, childOverrides := 0, 0, 0, 0
	for _, key := range keys {
		manifest := manifests[key]
		for _, phase := range manifest.PhaseDefs {
			for _, invoked := range []bool{false, true} {
				mode := "root"
				if invoked {
					mode = "child"
				}
				t.Run(key+"/"+phase.ID+"/"+mode, func(t *testing.T) {
					run := seedCompletionInvariantRun(t, h, dir, manifest, phase.ID, invoked)
					ctx := h.OwnerCtx(t, t.Context())
					effective, ok := manifest.PhaseForRun(run, phase.ID)
					if !ok {
						t.Fatal("catalog phase missing from effective contract")
					}
					if invoked && phase.ChildCompleteWhen != "" {
						childOverrides++
					}
					met, _, gateErr := h.Workflows.Manager.Phases.Gates.PhaseGateMet(ctx, manifest, run, map[string]any{})
					requiresProof := effective.Closeout.Gated() || effective.ReviewLoop != nil
					wantBlocked := requiresProof && (gateErr != nil || !met)
					assertCatalogSettlement(t, h, run, wantBlocked)
					if wantBlocked {
						blocked++
					} else {
						ordinary++
					}
					checked++

					// A durable human wait parks work without satisfying its gates.
					vars := runstate.SetFeedbackPending(map[string]any{}, "catalog-question", "Choose a direction.")
					testutil.FailErr(t, "persist human wait", h.Workflows.Manager.Store.State.UpdateVars(ctx, run, dir, vars))
					assertCatalogSettlement(t, h, run, false)
					testutil.FailErr(t, "clear human wait", h.Workflows.Manager.Store.State.UpdateVars(ctx, run, dir, map[string]any{}))
					assertCatalogSettlement(t, h, run, wantBlocked)
					stored, err := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
					testutil.FailErr(t, "read retained phase", err)
					if stored.CurrentPhase != phase.ID || stored.Status != api.WorkflowRunStatusRunning {
						t.Fatalf("turn settlement mutated workflow authority: %+v", stored)
					}
				})
			}
		}
	}
	if checked == 0 || blocked == 0 || ordinary == 0 || childOverrides == 0 {
		t.Fatalf("incomplete catalog coverage: phases=%d blocked=%d ordinary=%d child overrides=%d", checked, blocked, ordinary, childOverrides)
	}
	t.Logf("checked %d phase contracts: %d blocked, %d ordinary, %d child overrides", checked, blocked, ordinary, childOverrides)
}

func seedCompletionInvariantRun(t *testing.T, h *Harness, dir string, manifest workflowdef.Manifest, phase string, invoked bool) *api.WorkflowRun {
	t.Helper()
	ctx := h.OwnerCtx(t, t.Context())
	sess, err := h.Store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureSpec}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create catalog session", err)
	now := time.Now().UTC()
	run := &api.WorkflowRun{ID: uuid.NewString(), SessionID: sess.ID, WorkflowID: manifest.ID, WorkflowVersion: manifest.Version, CurrentPhase: phase, Status: api.WorkflowRunStatusRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if invoked {
		parent := *run
		parent.ID = uuid.NewString()
		parent.Status = api.WorkflowRunStatusPausedOnChild
		testutil.FailErr(t, "create parent identity", h.Workflows.Manager.Store.State.CreateState(ctx, &parent, dir, map[string]any{}))
		run.ParentRunID = &parent.ID
	}
	testutil.FailErr(t, "create phase fixture", h.Workflows.Manager.Store.State.CreateState(ctx, run, dir, map[string]any{}))
	return run
}

func assertCatalogSettlement(t *testing.T, h *Harness, run *api.WorkflowRun, blocked bool) {
	t.Helper()
	ctx := h.OwnerCtx(t, t.Context())
	sess, err := h.Store.Get(ctx, run.SessionID)
	testutil.FailErr(t, "read settlement project", err)
	stream, unsubscribe, err := h.Events.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to settlement", err)
	defer unsubscribe()
	testutil.FailErr(t, "settle catalog turn", h.Sessions.Manager.Runner.Settlement.Settle(ctx, run.SessionID, api.SessionIdleDispositionCompleted))
	want := api.SessionIdleDispositionCompleted
	if blocked {
		want = api.SessionIdleDispositionTurnError
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-stream:
			raw, err := json.Marshal(event.Data)
			testutil.FailErr(t, "encode session event", err)
			var status api.SessionEvent
			if json.Unmarshal(raw, &status) != nil || status.ID != run.SessionID || status.Status != api.SessionStatusIdle || status.IdleDisposition == "" {
				continue
			}
			if status.IdleDisposition != want {
				t.Fatalf("catalog settlement=%s, want %s", status.IdleDisposition, want)
			}
			return
		case <-deadline:
			t.Fatal("missing catalog settlement event")
		}
	}
}
