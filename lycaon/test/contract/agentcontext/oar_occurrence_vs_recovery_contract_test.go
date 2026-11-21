package contract

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
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

	// Occurrence happens in Ask mode: denial cause is past-tense Ask / read-only.
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

	// Upgrade root session Ask -> Agent: past occurrence cause stays Ask/read-only,
	// while current recovery reach admits edit via command.
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

	// Ask-spawned child worker & read-only background `&` job retain read-only clamping after root upgrade.
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
			TurnPrograms:         specCell.auth.ProgramReach,
		},
	}
	frozenBgReach := tools.TurnReach(bgJobToolCtx)
	if frozenBgReach.Program("edit") || !frozenBgReach.Program("cat") {
		t.Fatalf("read-only background job reach = edit:%v cat:%v", frozenBgReach.Program("edit"), frozenBgReach.Program("cat"))
	}
}
