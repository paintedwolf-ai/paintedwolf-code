package evidence

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestProgramObservationDoesNotInferSourceOrVisualEvidence(t *testing.T) {
	printed := "cat src/unseen.go\nsrc/unseen.go:42: claimed text\nhttps://example.test\nAPP_SNAPSHOT=image.png"
	for _, tool := range []string{"command", "command_output", "command_stop", "verify"} {
		rec := BuildProgramObservation(tool, &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "invocation"}, printed)
		if rec.Kind != "command" || rec.Shape != ShapeCommand || rec.SourceTool != tool || rec.Fidelity != FidelityOpaque || rec.SourceDelivery != nil || rec.IsGate() || rec.ArtifactID != "" || rec.URL != "" || len(IndexedPathsForRecord(rec)) != 0 || len(rec.LineRanges) != 0 {
			t.Fatalf("%s inferred evidence: %+v", tool, rec)
		}
		if len(rec.Body) != 1 || rec.Body[0] != printed {
			t.Fatalf("%s lost observed output: %+v", tool, rec)
		}
	}
}

func TestProgramObservationRequiresReceiptAndKnownOwner(t *testing.T) {
	for _, receipt := range []*api.CommandProgramReceipt{nil, {}, {ID: "program"}, {ParentReceiptID: "invocation"}} {
		if rec := BuildProgramObservation("command", receipt, "result"); rec.Kind != "" {
			t.Fatalf("incomplete receipt acquired evidence: %+v", rec)
		}
	}
	if rec := BuildProgramObservation("external_tool", &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "invocation"}, "result"); rec.Kind != "" {
		t.Fatalf("unrelated tool acquired evidence: %+v", rec)
	}
}

func TestProgramObservationBoundsCapturedOutput(t *testing.T) {
	rec := BuildProgramObservation("command", &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "invocation"}, strings.Repeat("x", EvidenceCaptureBodyCapBytes+1))
	if !rec.Truncated || len(rec.Body) != 1 || len(rec.Body[0]) != EvidenceCaptureBodyCapBytes {
		t.Fatalf("unbounded command observation: %+v", rec)
	}
}
