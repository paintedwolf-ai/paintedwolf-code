package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Producer classifications are fixtures here. The destination is the actual
// document/publication owner; native grep/diff entry qualification belongs to
// their adapters and connected runtime, not this status-layer acceptance test.
func TestCommandPipelinePublicationGatePreservesComparisonAndDestination(t *testing.T) {
	comparison := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, PublicationBasis: toolcontract.NativeComparisonPublication, EffectCertainty: toolcontract.NoEffect}
	external := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectUnknown}
	failed := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
	success := toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}
	for _, fixture := range []struct {
		name, output string
		producers    []toolcontract.ProducerResult
		publish      bool
		status       int
	}{
		{"native no-match empty result", "", []toolcontract.ProducerResult{comparison}, true, 1},
		{"native differences", "synthetic complete differences\n", []toolcontract.ProducerResult{comparison}, true, 1},
		{"unclassified external prefix", "must not publish\n", []toolcontract.ProducerResult{external}, false, 1},
		{"earlier failure then successful final stage", "must not publish\n", []toolcontract.ProducerResult{failed, success}, false, 2},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			adapter, _, project, rootID := newAdapterFixture(t)
			before, ok, err := adapter.OpenDocument(t.Context(), project.ID, project.SourceBranch, rootID, "a.txt")
			testutil.FailErr(t, "capture destination", err)
			if !ok {
				t.Fatal("destination capture absent")
			}
			stages := make([]commandprogram.PipelineStageOutcome, len(fixture.producers))
			for i, result := range fixture.producers {
				stages[i] = commandprogram.PipelineStageOutcome{ExecutionID: uuid.NewString(), Result: result, RequiredInputComplete: true}
			}
			latch := &commandprogram.OutcomeLatch{}
			settled, err := commandprogram.SettlePipeline("fixture-pipeline", stages, latch)
			testutil.FailErr(t, "settle complete producer pipeline", err)
			if settled.Status != fixture.status || settled.PublicationEligible != fixture.publish {
				t.Fatalf("settlement=%+v", settled)
			}
			reviews := 0
			if settled.PublicationEligible {
				edit := tools.EditorDocumentEdit{DocumentID: before.ID, ExpectedRevision: before.Revision, Content: fixture.output, OperationID: uuid.NewString(), SessionID: "fixture-status-session", ToolName: "command"}
				previews, err := adapter.PreviewAgentEdits(t.Context(), project.ID, []tools.EditorDocumentEdit{edit})
				testutil.FailErr(t, "prepare exact publication", err)
				tc := tools.ToolContext{}
				tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { reviews++; return nil }
				testutil.FailErr(t, "review publication", tc.ReviewFileChanges(t.Context(), tools.FileChange{Path: filepath.Join(project.Roots[0].Path, "a.txt"), Preview: api.ApprovalFileChange{Path: "a.txt", RootID: rootID, Operation: "write", Before: before.Text, After: previews[0].After, BeforeSHA256: before.SHA256, AfterSHA256: previews[0].AfterSHA256}}))
				edit.ReviewedRevision = previews[0].Before.Revision
				applied, err := adapter.ApplyAgentEdit(t.Context(), project.ID, edit)
				testutil.FailErr(t, "publish through document owner", err)
				if !applied.Saved || applied.PublicationError != nil {
					t.Fatalf("publication=%+v", applied)
				}
			}
			after, _, err := adapter.OpenDocument(t.Context(), project.ID, project.SourceBranch, rootID, "a.txt")
			testutil.FailErr(t, "inspect accepted destination", err)
			actual, err := os.ReadFile(filepath.Join(project.Roots[0].Path, "a.txt"))
			testutil.FailErr(t, "inspect published bytes", err)
			expected := "base\n"
			if fixture.publish {
				expected = fixture.output
				if reviews != 1 || after.Revision <= before.Revision {
					t.Fatal("publication lost review/revision")
				}
			} else if reviews != 0 || after.Revision != before.Revision {
				t.Fatal("ineligible output changed destination")
			}
			if string(actual) != expected || after.Text != expected {
				t.Fatalf("accepted=%q published=%q expected=%q", after.Text, actual, expected)
			}
			flow, err := commandprogram.NewStatusControl(latch)
			testutil.FailErr(t, "create conditional state", err)
			testutil.FailErr(t, "record final ordinary pipeline status", flow.Record("fixture-pipeline-control", toolcontract.ProducerResult{Status: settled.Status, Terminal: toolcontract.ProducerCompleted, Complete: settled.Complete, EffectCertainty: toolcontract.EffectUnknown}, true, false))
			if run, err := flow.ShouldRun(commandprogram.StatusAnd); err != nil || run {
				t.Fatal("nonzero comparison/failure took true branch")
			}
			if run, err := flow.ShouldRun(commandprogram.StatusOr); err != nil || !run {
				t.Fatal("ordinary nonzero could not select false branch")
			}
		})
	}
}
