package guidance

import (
	"strings"
	"testing"
)

func TestStripHostBlocksStripsRejectBlocks(t *testing.T) {
	in := ">>> Spec posture blocked\nRejected: no\nCode: SPEC_POSTURE_STUB_REQUIRED\nDo the implementation."
	out := StripHostBlocks(in)
	if strings.Contains(out, ">>>") || strings.Contains(out, "Rejected:") || strings.Contains(out, "Code: SPEC_") {
		t.Fatalf("worker prompt still has coordinator markers:\n%s", out)
	}
	if !strings.Contains(out, "Do the implementation.") {
		t.Fatalf("expected task body preserved:\n%s", out)
	}
}

func TestStripHostBlocksStripsHostBlocksOnly(t *testing.T) {
	in := "Fix the bug.\n" + markerRequiredNext + "\n" +
		"Call task(agent_type=implementer, brief={goal:\"x\",done_when:[\"done\"]})\n" +
		"Code: COORDINATOR_BATCH_ALREADY_CLOSED"
	out := StripHostBlocks(in)
	if strings.Contains(out, markerRequiredNext) || strings.Contains(out, "COORDINATOR_BATCH_ALREADY_CLOSED") {
		t.Fatalf("coordinator view kept a host block:\n%s", out)
	}
	if !strings.Contains(out, "Fix the bug.") {
		t.Fatal("expected task summary preserved")
	}
	// Prose the coordinator itself wrote is not a host block, and the host does
	// not read it to decide what it meant.
	if !strings.Contains(out, "Call task(agent_type=implementer") {
		t.Fatalf("coordinator-authored line was dropped:\n%s", out)
	}
}

func TestStripHostBlocksPreservesContractFormatting(t *testing.T) {
	contract := "Shared interface:\n```\ndef render(state):\n    return state.text\n\n    # preserve nested examples\n```"
	if got := StripHostBlocks(markerRequiredNext + "\n" + contract); got != contract {
		t.Fatalf("contract changed: %q", got)
	}
}
