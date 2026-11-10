package workercompletion_test

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAcceptedWorkerReportSurvivesStoreReload(t *testing.T) {
	database := testdbfixture.Open(t, "accepted-report.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	writer := store.NewSQL(database)
	sess, err := writer.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create report session", err)
	receipt := &api.WorkerReportAcceptance{
		WorkerID:  "job",
		Report:    &api.WorkerCompletionReport{LegStatus: "partial", Brief: "Bounded inspection", Verification: &api.CitationVerification{Method: "inspection", Reason: "Read the assigned source"}, Findings: []api.WorkerCompletionFinding{{Path: "main.go", Line: 7, Excerpt: "return nil", Cite: "[main.go:7](main.go#L7)"}}},
		Grounding: &api.CitationGrounding{Traced: true},
	}
	testutil.FailErr(t, "persist accepted report", writer.AppendMessages(t.Context(), sess.ID, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "complete_leg", Outcome: api.ToolResultOutcomeCompleted, WorkerReport: receipt, Content: "compacted acknowledgement"}}))
	messages, err := store.NewSQL(database).GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "reload accepted report", err)
	got, _, ok := workercompletion.AcceptedWorkerReport(messages, "job")
	if !ok || !reflect.DeepEqual(got, receipt) {
		t.Fatalf("accepted report changed after reload: got=%+v want=%+v", got, receipt)
	}
}
