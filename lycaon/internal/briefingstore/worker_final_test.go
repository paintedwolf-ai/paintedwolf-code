package briefingstore

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	roundJob      = "22222222-2222-4222-8222-222222222222"
	laterRoundJob = "33333333-3333-4333-8333-333333333333"
	otherJob      = "44444444-4444-4444-8444-444444444444"
)

// The final read is the newest assistant row of one job in its own session;
// another job's rows, a later round's rows and the parent's rows never answer.
func TestWorkerFinalReadIsTheRoundsOwnNewestOutput(t *testing.T) {
	database := testdbfixture.Open(t, "briefing-worker-final.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	sessions := store.NewSQL(database)
	parent, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create child", err)
	for _, job := range []string{roundJob, laterRoundJob, otherJob} {
		_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, child_session_id, agent_type, status, prompt, brief, created_at)
			VALUES (?, ?, ?, 'repo-researcher', 'failed', 'fixture', 'fixture', ?)`, job, testdbseed.DefaultProjectID, child.ID, time.Now().UTC().Format(time.RFC3339Nano))
		testutil.FailErr(t, "insert worker job", err)
	}
	thinking := func(text string) *api.ModelReasoning { return &api.ModelReasoning{Text: text} }
	testutil.FailErr(t, "append child", sessions.AppendMessages(t.Context(), child.ID,
		api.Message{ID: "early", Role: api.MessageRoleAssistant, Content: "start", WorkerID: roundJob, ModelReasoning: thinking("first look")},
		api.Message{ID: "final", Role: api.MessageRoleAssistant, Content: "stopping", WorkerID: roundJob, ModelReasoning: thinking("next: rerun the parser tests")},
		api.Message{ID: "tool-after", Role: api.MessageRoleUser, Content: "tool output", WorkerID: roundJob},
		api.Message{ID: "later-round", Role: api.MessageRoleAssistant, Content: "later", WorkerID: laterRoundJob, ModelReasoning: thinking("later round")},
		api.Message{ID: "other-job", Role: api.MessageRoleAssistant, Content: "other", WorkerID: otherJob, ModelReasoning: thinking("other job")},
	))
	testutil.FailErr(t, "append parent", sessions.AppendMessages(t.Context(), parent.ID,
		api.Message{ID: "parent-row", Role: api.MessageRoleAssistant, Content: "coordinating", WorkerID: roundJob, ModelReasoning: thinking("parent")},
	))

	reader := NewCausal(database)
	read, err := reader.ReadWorkerFinal(t.Context(), child.ID, roundJob)
	testutil.FailErr(t, "read final", err)
	if !read.Found || read.OutputID != "final" || read.Reasoning.Tail != "next: rerun the parser tests" {
		t.Fatalf("final read %+v", read)
	}
	input := read.ReasoningInput()
	if input.Excerpt == nil || input.Excerpt.Ref != "message:final" || input.Excerpt.Shared {
		t.Fatalf("input %+v", input)
	}
	missing, err := reader.ReadWorkerFinal(t.Context(), parent.ID, laterRoundJob)
	testutil.FailErr(t, "read a job with no rows in the session", err)
	if missing.Found || missing.ReasoningInput() != (briefing.ReasoningInput{}) {
		t.Fatalf("a job with no output in the session read %+v", missing)
	}
}
