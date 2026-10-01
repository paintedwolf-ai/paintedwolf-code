package inject_test

import (
	anchortestsetup "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMain(m *testing.M) {
	anchortestsetup.Install()
	code := m.Run()
	anchor.SetDefaultRegistry(nil)
	anchorcatalog.Clear()
	os.Exit(code)
}

func TestFilterSuppressedHintCodesGateBlocked(t *testing.T) {
	codes := []string{"WORKFLOW_GATE_UNMET", "OTHER"}
	out := anchor.FilterSuppressedHintCodes(codes, api.CoordinatorRunContext{}, string(anchor.GateBlocked))
	if len(out) != 1 || out[0] != "OTHER" {
		t.Fatalf("out=%v", out)
	}
}

func TestFilterSuppressedHintCodesComposeDoneRequiresBrief(t *testing.T) {
	codes := []string{"WORKFLOW_SESSION_COMPOSE_REQUIRED", "OTHER"}
	out := anchor.FilterSuppressedHintCodes(codes, api.CoordinatorRunContext{}, string(anchor.ComposeDone))
	if len(out) != 2 {
		t.Fatalf("without brief should keep compose hint: %v", out)
	}
	out = anchor.FilterSuppressedHintCodes(codes, api.CoordinatorRunContext{CoordinatorBrief: "brief"}, string(anchor.ComposeDone))
	if len(out) != 1 || out[0] != "OTHER" {
		t.Fatalf("with brief out=%v", out)
	}
}

func TestFilterSuppressedHintCodesFeedbackPending(t *testing.T) {
	codes := []string{"WORKFLOW_FEEDBACK_PENDING", "OTHER"}
	out := anchor.FilterSuppressedHintCodes(codes, api.CoordinatorRunContext{}, string(anchor.FeedbackPending))
	if len(out) != 1 || out[0] != "OTHER" {
		t.Fatalf("out=%v", out)
	}
}

func TestFilterSuppressedHintCodesNoPendingPassthrough(t *testing.T) {
	codes := []string{"WORKFLOW_GATE_UNMET"}
	out := anchor.FilterSuppressedHintCodes(codes, api.CoordinatorRunContext{}, "")
	if len(out) != 1 {
		t.Fatalf("out=%v", out)
	}
}

func TestOmitInformWhenBoardReinjected(t *testing.T) {
	if !anchor.OmitInformWhenBoardReinjected(anchor.LegFinished) {
		t.Fatal("leg.finished must omit inform when board reinjected")
	}
}
