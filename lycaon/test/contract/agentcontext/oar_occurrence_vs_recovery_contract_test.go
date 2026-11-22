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
	if !ok || strings.Contains(cmdOpAskRef.Copy["fix"], "`command") {
		t.Fatalf("Ask-turn edit refusal must not recommend command: %v", cmdOpAskRef)
	}
	pastCause := cmdOpAskRef.Copy["cause"]
	if !strings.Contains(pastCause, "cannot run a replacement form") {
		t.Fatalf("unexpected past-tense Ask denial cause: %q", pastCause)
	}

	rule, _ := h.rules.Get("TOOL_COMMAND_OPERATION")
	recoveryGC := oar.NewGuardContext()
	recoveryGC.Reach = agentCell.auth.Reach
	recoveryGC.ObservationData = map[string]any{"need": "edit"}
	recoveryGC.ObserveCommandOperationReach()
	steps := oar.ResolveNextSteps(rule, recoveryGC)
	if !strings.Contains(steps.Text, "`command(…)`") || len(steps.Calls) == 0 {
		t.Fatalf("upgraded Agent recovery reach failed to offer command: %+v", steps)
	}
	if cmdOpAskRef.Copy["cause"] != pastCause {
		t.Fatal("upgrading session mutated stored Ask occurrence cause")
	}

	workerStates, wErr := hoststate.WorkerStates(t.Context())
	contractcheck.FailErr(t, "WorkerStates", wErr)
	checkedAskChild := false
	for _, ws := range workerStates {
		if ws.Excluded || ws.RootCount == 0 || ws.Posture != api.SessionPostureBuild || ws.ParentSessionMode != api.SessionModeAsk || !ws.ParentUpgradedAskToAgent || ws.ToolProfile != "implement" {
			continue
		}
		childAuth, fErr := hoststate.FreezeWorkerAuthority(t.Context(), ws)
		contractcheck.FailErr(t, "FreezeWorkerAuthority Ask child", fErr)
		if childAuth.Reach.Program("edit") || childAuth.ProgramReach("edit") || !childAuth.ProgramReach("cat") {
			t.Fatalf("Ask-spawned child worker lost read-only program clamp: reachEdit=%v progEdit=%v progCat=%v",
				childAuth.Reach.Program("edit"), childAuth.ProgramReach("edit"), childAuth.ProgramReach("cat"))
		}
		if slices.Contains(childAuth.Offered, "code_rewrite") || childAuth.Reader.Held()[toolcontract.ReachCommandMutation] {
			t.Fatalf("Ask-spawned child worker offered mutating tools after parent upgrade: %v", childAuth.Offered)
		}
		checkedAskChild = true
		break
	}
	if !checkedAskChild {
		t.Fatal("expected realizable implement worker state under Ask->Agent upgraded parent")
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

// TestSandboxPathRejectionRequiresConfirmedHumanActorForUserDeclinedCopy proves
// through the production WriteRootCheckpointBroker -> CommandServices -> Executor
// -> BlockPlane chain that only ResolvedByHuman rejections emit user-declined copy.
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
			}, isolation.CodeWriteRootDenied, actor.human, actor.comments)

			assertProductionPathCall(t, host, projectDir, "read_path", map[string]any{
				"command":            "ls",
				"snapshot_capture":   map[string]any{},
				"capability_request": map[string]any{"read_path": keyPath},
			}, isolation.CodeReadPathDenied, actor.human, actor.comments)

			assertProductionPathCall(t, host, projectDir, "multi_read_path", map[string]any{
				"command":            "ls",
				"snapshot_capture":   map[string]any{},
				"capability_request": map[string]any{"read_path": []any{keyPath, certPath}},
			}, isolation.CodeReadPathDenied, actor.human, actor.comments)
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
	if wantHuman {
		if refusal.Code() != humanDeniedCode {
			t.Fatalf("%s human rejection code = %q, want %q", label, refusal.Code(), humanDeniedCode)
		}
		if comments != "" && !strings.Contains(rendered, comments) {
			t.Errorf("%s human rejection lost guidance %q:\n%s", label, comments, rendered)
		}
		if comments == "" && !strings.Contains(rendered, "Wait for new user direction") {
			t.Errorf("%s unguided human rejection missing user-declined copy:\n%s", label, rendered)
		}
		return
	}
	if refusal.Code() != isolation.CodeApprovalUnavailable {
		t.Fatalf("%s nonhuman rejection code = %q, want %q:\n%s", label, refusal.Code(), isolation.CodeApprovalUnavailable, rendered)
	}
	if !strings.Contains(rendered, "rejected without a confirmed user decision") {
		t.Errorf("%s nonhuman rejection missing neutral cause:\n%s", label, rendered)
	}
	for _, forbidden := range []string{"Wait for new user direction", "Follow the user", "must not leak"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("%s nonhuman rejection leaked user-declined copy %q:\n%s", label, forbidden, rendered)
		}
	}
}
