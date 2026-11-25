package contract

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settings/settingstest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func verifyOccurrenceVsRecoveryAuthority(t *testing.T, h *hookProducerHarness, cells []roleCell) {
	t.Helper()
	var askCell, specCell, agentCell *roleCell
	for i := range cells {
		if cells[i].role == "coordinator" && cells[i].posture == api.SessionPostureBuild && cells[i].mode == api.SessionModeAsk {
			askCell = &cells[i]
		}
		if cells[i].role == "coordinator" && cells[i].posture == api.SessionPostureSpec && cells[i].mode == api.SessionModeAgent {
			specCell = &cells[i]
		}
		if cells[i].role == "coordinator" && cells[i].posture == api.SessionPostureBuild && cells[i].mode == api.SessionModeAgent && cells[i].auth.Reach.Program("edit") {
			agentCell = &cells[i]
		}
	}
	if askCell == nil || specCell == nil || agentCell == nil {
		t.Fatalf("missing coordinator cells: ask=%v spec=%v agent=%v", askCell != nil, specCell != nil, agentCell != nil)
	}

	askCtx := askCell.ctx(t.Context())
	askRefusal, blocked, err := h.policy.Block(askCtx, oar.AnchorSessionPreInvoke, askCell.sess, "task", nil, func(g *oar.GuardContext) error {
		g.Workers.AskWorkerWrites = true
		return nil
	})
	contractcheck.FailErr(t, "Ask occurrence Block", err)
	if !blocked || askRefusal == nil || askRefusal.Code() != "SESSION_ASK_MODE_ACTIVE" {
		t.Fatalf("expected SESSION_ASK_MODE_ACTIVE in Ask mode, got %v", askRefusal)
	}
	cmdOpAskErr := h.blockPlane.RejectObservation(askCtx, "edit", "coordinator", nil, &toolrejection.ToolReject{
		Code: "TOOL_COMMAND_OPERATION", Data: map[string]any{"need": "edit"},
	})
	cmdOpAskRef, ok := guidance.RefusalFromError(cmdOpAskErr)
	if !ok || cmdOpAskRef.Code() != "TOOL_COMMAND_OPERATION" {
		t.Fatalf("Ask-turn edit refusal = %v, want TOOL_COMMAND_OPERATION", cmdOpAskErr)
	}
	if got := nextCallTools(cmdOpAskRef); slices.Contains(got, "command") {
		t.Fatalf("Ask-turn edit refusal next calls = %v, want no command", got)
	}
	pastCause := cmdOpAskRef.Copy["cause"]

	rule, _ := h.rules.Get("TOOL_COMMAND_OPERATION")
	recoveryGC := oar.NewGuardContext()
	recoveryGC.Reach = agentCell.auth.Reach
	recoveryGC.ObservationData = map[string]any{"need": "edit"}
	recoveryGC.ObserveCommandOperationReach()
	steps := oar.ResolveNextSteps(t.Context(), rule, recoveryGC)
	if !slices.ContainsFunc(steps.Calls, func(c toolreach.Call) bool { return c.Tool == "command" }) {
		t.Fatalf("upgraded Agent recovery reach next calls = %+v, want command", steps.Calls)
	}
	if cmdOpAskRef.Copy["cause"] != pastCause {
		t.Fatal("upgrading session mutated stored Ask occurrence cause")
	}

	workerStates, wErr := hoststate.WorkerStates(t.Context())
	contractcheck.FailErr(t, "WorkerStates", wErr)
	checkedRealizableAskChild := false
	checkedMutatingChildClamp := false
	for _, ws := range workerStates {
		if ws.RootCount == 0 || ws.Posture != api.SessionPostureBuild || ws.ParentSessionMode != api.SessionModeAsk || !ws.ParentUpgradedAskToAgent {
			continue
		}
		if !ws.Excluded && !checkedRealizableAskChild {
			childAuth, fErr := hoststate.FreezeWorkerAuthority(t.Context(), ws)
			contractcheck.FailErr(t, "FreezeWorkerAuthority Ask child", fErr)
			if childAuth.Reach.Program("edit") || childAuth.ProgramReach("edit") || !childAuth.ProgramReach("cat") {
				t.Fatalf("Ask-spawned child worker lost read-only program clamp: reachEdit=%v progEdit=%v progCat=%v",
					childAuth.Reach.Program("edit"), childAuth.ProgramReach("edit"), childAuth.ProgramReach("cat"))
			}
			if slices.Contains(childAuth.Offered, "code_rewrite") || childAuth.Reader.Held()[toolcontract.ReachCommandMutation] {
				t.Fatalf("Ask-spawned child worker offered mutating tools after parent upgrade: %v", childAuth.Offered)
			}
			checkedRealizableAskChild = true
		}
		if ws.ToolProfile == "implement" && !checkedMutatingChildClamp {
			if !ws.Excluded || ws.ExcludedReason != "ask_mode_forbids_mutating_worker" {
				t.Fatalf("expected implement worker under Ask parent to be excluded with ask_mode_forbids_mutating_worker, got excluded=%v reason=%q",
					ws.Excluded, ws.ExcludedReason)
			}
			clampAuth, fErr := hoststate.FreezeWorkerAuthority(t.Context(), ws)
			contractcheck.FailErr(t, "FreezeWorkerAuthority Ask mutating clamp", fErr)
			if clampAuth.Reach.Program("edit") || clampAuth.ProgramReach("edit") || !clampAuth.ProgramReach("cat") {
				t.Fatalf("FreezeWorkerAuthority Ask clamp lost read-only program restriction: reachEdit=%v progEdit=%v progCat=%v",
					clampAuth.Reach.Program("edit"), clampAuth.ProgramReach("edit"), clampAuth.ProgramReach("cat"))
			}
			if slices.Contains(clampAuth.Offered, "code_rewrite") || clampAuth.Reader.Held()[toolcontract.ReachCommandMutation] {
				t.Fatalf("FreezeWorkerAuthority Ask clamp offered mutating tools after parent upgrade: %v", clampAuth.Offered)
			}
			checkedMutatingChildClamp = true
		}
		if checkedRealizableAskChild && checkedMutatingChildClamp {
			break
		}
	}
	if !checkedRealizableAskChild || !checkedMutatingChildClamp {
		t.Fatalf("missing Ask->Agent child worker witnesses: realizable=%v mutatingClamp=%v",
			checkedRealizableAskChild, checkedMutatingChildClamp)
	}

	bgJobToolCtx := tools.ToolContext{
		Turn: tools.InvocationTurn{
			TurnOfferedToolNames: specCell.auth.Offered,
			TurnToolPlan:         specCell.auth.Plan,
			TurnPrograms:         toolreach.TableFromPrograms(specCell.auth.ProgramReach),
		},
	}
	frozenBgReach := tools.TurnReach(bgJobToolCtx)
	if frozenBgReach.Program("edit") || !frozenBgReach.Program("cat") {
		t.Fatalf("read-only background job reach = edit:%v cat:%v", frozenBgReach.Program("edit"), frozenBgReach.Program("cat"))
	}
}

type actorCheckpointStub struct {
	resolvedBy string
	comments   string
}

func (s *actorCheckpointStub) RequestCheckpoint(context.Context, hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: "cp-actor", Status: hitl.DecisionStatusPending}, nil
}

func (s *actorCheckpointStub) PollCheckpoint(context.Context, string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{
		CheckpointID: "cp-actor",
		Status:       hitl.DecisionStatusRejected,
		ResolvedBy:   s.resolvedBy,
		Result:       &hitl.DecisionResult{Approved: false, Comments: s.comments},
	}, nil
}

func (*actorCheckpointStub) ResolveCheckpoint(context.Context, string, string, api.CheckpointKind, *hitl.DecisionResult, *hitl.ContentApplyResolve) (*hitl.CheckpointResponse, error) {
	return nil, nil
}
func (*actorCheckpointStub) ListPendingForParent(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (*actorCheckpointStub) ListPending(context.Context, string, *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	return nil, nil
}
func (*actorCheckpointStub) OldestPendingCheckpoints(context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (*actorCheckpointStub) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}
func (*actorCheckpointStub) SessionApprovalDenied(context.Context, string) (bool, error) {
	return false, nil
}
func (*actorCheckpointStub) PatchPendingToolApprovalAIRationale(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (*actorCheckpointStub) ClearPendingToolApprovalAIRationale(context.Context, string) error {
	return nil
}
func (*actorCheckpointStub) PatchPendingToolApprovalJoined(context.Context, string, int, []string, string, string) error {
	return nil
}

// Through the production checkpoint broker, executor and block plane, only a
// rejection resolved by a human carries the human-denied code and the user's
// comments; other actors get the approval-unavailable code without them.
func TestSandboxPathRejectionRequiresConfirmedHumanActorForUserDeclinedCopy(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_BYPASS", "")
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), "isolated-state"))
	_, _, _, bp := effectiveOARBlockPlane(t)
	extCatalog, err := extpacks.CatalogForConsumers()
	contractcheck.FailErr(t, "resolve effective pack catalog", err)
	catalog, err := sensitivepath.Load(sensitivepath.Bundled())
	contractcheck.FailErr(t, "load sensitive path catalog", err)

	projectDir := t.TempDir()
	outsideWrite := filepath.Join(t.TempDir(), "outside-write")
	protectedDir := t.TempDir()
	keyPath := filepath.Join(protectedDir, "id_ed25519")
	certPath := filepath.Join(protectedDir, "cacert.pem")

	actors := []struct {
		name       string
		resolvedBy string
		comments   string
		human      bool
	}{
		{name: "human_guided", resolvedBy: authzledger.ResolvedByHuman, comments: "Stay inside the repository.", human: true},
		{name: "human_unguided", resolvedBy: authzledger.ResolvedByHuman, comments: "", human: true},
		{name: "missing_actor", resolvedBy: "", comments: "unattributed note must not leak", human: false},
		{name: "system_deny_actor", resolvedBy: authzledger.ResolvedBySystemDeny, comments: "system note must not leak", human: false},
	}

	for _, actor := range actors {
		t.Run(actor.name, func(t *testing.T) {
			cp := &actorCheckpointStub{resolvedBy: actor.resolvedBy, comments: actor.comments}
			store, sErr := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
			contractcheck.FailErr(t, "NewApprovalStoreAt", sErr)
			authority := settings.NewRuleApprovalGate(store, settingstest.EmptySources())

			host, hErr := toolhost.NewRuntime(toolhost.RuntimeConfig{Catalog: extCatalog})
			contractcheck.FailErr(t, "toolhost.NewRuntime", hErr)
			host.Executor.Rejections.SetBlockPlane(bp)
			host.Executor.Approvals.SetCheckpointManager(t.Context(), cp, authority)
			broker := &session.WriteRootCheckpointBroker{
				Checkpoints: cp,
				Runtime:     approvalstate.NewSandboxPathGrantRuntime(),
				ReadRuntime: approvalstate.NewSandboxPathGrantRuntime(),
				Authority:   authority,
				Locations:   catalog,
				Posture:     func(string) gate.Posture { return gate.PostureBalanced },
			}
			host.Commands.SetSandboxWriteRootGate(broker)

			assertProductionPathCall(t, host, projectDir, "write_root", map[string]any{
				"command":            "ls",
				"snapshot_capture":   map[string]any{},
				"capability_request": map[string]any{"write_root": outsideWrite},
			}, isolation.CodeWriteRootDenied, actor.human, actor.comments, actor.resolvedBy == authzledger.ResolvedBySystemDeny)

			assertProductionPathCall(t, host, projectDir, "read_path", map[string]any{
				"command":            "ls",
				"snapshot_capture":   map[string]any{},
				"capability_request": map[string]any{"read_path": keyPath},
			}, isolation.CodeReadPathDenied, actor.human, actor.comments, actor.resolvedBy == authzledger.ResolvedBySystemDeny)

			assertProductionPathCall(t, host, projectDir, "multi_read_path", map[string]any{
				"command":            "ls",
				"snapshot_capture":   map[string]any{},
				"capability_request": map[string]any{"read_path": []any{keyPath, certPath}},
			}, isolation.CodeReadPathDenied, actor.human, actor.comments, actor.resolvedBy == authzledger.ResolvedBySystemDeny)
		})
	}
}

func assertProductionPathCall(
	t *testing.T,
	host *toolhost.Runtime,
	projectDir, label string,
	args map[string]any,
	humanDeniedCode string,
	wantHuman bool,
	comments string,
	refusedAutomatically bool,
) {
	t.Helper()
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "sess-" + label,
			ProjectID:  "proj-witness",
			ToolCallID: "tc-" + label,
			Agent:      "implement",
		},
		Source: tools.InvocationSource{
			Roots:        []projectroot.RootRef{{ID: "root", Path: projectDir, IsPrimary: true}},
			ActiveRootID: "root",
		},
	}
	_, err := host.Executor.Invoke(t.Context(), "command", args, tc)
	refusal, ok := guidance.RefusalFromError(err)
	if !ok || refusal == nil {
		t.Fatalf("%s Invoke err = %v, want curated guidance.Refusal", label, err)
	}
	rendered := refusal.Error() + "\n" + refusal.Copy["what"] + "\n" + refusal.Copy["cause"] + "\n" + refusal.Copy["fix"]
	wantCode := isolation.CodeApprovalUnavailable
	if wantHuman {
		wantCode = humanDeniedCode
	}
	if refusal.Code() != wantCode {
		t.Fatalf("%s rejection code = %q, want %q", label, refusal.Code(), wantCode)
	}
	if reject := toolrejection.AsToolReject(err); reject == nil || (reject.Data["approval_refused_automatically"] == true) != refusedAutomatically {
		t.Errorf("%s rejection facts = %+v, want approval_refused_automatically=%t", label, reject, refusedAutomatically)
	}
	if comments != "" && strings.Contains(rendered, comments) != wantHuman {
		t.Errorf("%s user comments delivered=%t, want %t:\n%s", label, !wantHuman, wantHuman, rendered)
	}
}
