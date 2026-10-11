// Package property holds cross-package property tests.
package property

import (
	"context"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

func TestOneCanonicalWorkerCardAndSupersedeInPlace(t *testing.T) {
	ctx := context.Background()

	rapid.Check(t, func(t *rapid.T) {
		store := store.NewMemory()
		mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
		sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		failErr(t, "create session", err)

		jobID := uuid.NewString()
		canonicalID := uuid.NewString()
		enqueue := `{"job_id":"` + jobID + `","status":"enqueued"}`
		failErr(t, "seed canonical row", store.AppendMessages(ctx, sess.ID, api.Message{
			ID:         canonicalID,
			Role:       api.MessageRoleTool,
			Content:    enqueue,
			ToolResult: &api.ToolResult{Tool: "task", Content: enqueue, Dispatch: &api.WorkerDispatch{WorkerID: jobID}},
		}))
		canonicalOrd := mustFindMessage(t, ctx, store, sess.ID, canonicalID).Ord

		statusGen := rapid.SampledFrom(api.AllWorkerSummaryStatuses())
		stepCount := rapid.IntRange(1, 6).Draw(t, "steps")
		var lastStatus api.WorkerSummaryStatus
		var lastEnvelope string
		for i := 0; i < stepCount; i++ {
			lastStatus = statusGen.Draw(t, "status_"+strconv.Itoa(i))
			childSessionID := "child-" + jobID
			agentType := "implementer"
			envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
				JobID:          jobID,
				ChildSessionID: childSessionID,
				AgentType:      agentType,
				State:          string(lastStatus),
				Summary:        "x",
			})
			lastEnvelope = envelope
			failErr(t, "project worker card", mgr.Workers.Cards.Project(ctx, sess.ID, jobID, &api.WorkerSummaryMeta{
				WorkerID:       jobID,
				ChildSessionID: childSessionID,
				AgentType:      agentType,
				Status:         lastStatus,
				Envelope:       envelope,
			}))
		}

		all, err := store.GetMessages(ctx, sess.ID)
		failErr(t, "get messages", err)
		toolRows := 0
		var canonical *api.Message
		for i := range all {
			if all[i].Role == api.MessageRoleTool && all[i].ToolResult != nil &&
				all[i].ToolResult.Dispatch != nil && all[i].ToolResult.Dispatch.WorkerID == jobID {
				toolRows++
				canonical = &all[i]
			}
		}
		if toolRows != 1 {
			t.Fatalf("tool rows for job = %d want 1 — one canonical row per job, never a second (patched %d steps)", toolRows, stepCount)
		}
		if canonical.ID != canonicalID {
			t.Fatalf("canonical id mutated by patch: was %s now %s", canonicalID, canonical.ID)
		}
		if canonical.Ord != canonicalOrd {
			t.Fatalf("canonical ord mutated by patch: was %d now %d — ord is the immutable creation ordinal", canonicalOrd, canonical.Ord)
		}
		// The card carries the latest completion envelope.
		if canonical.Content != lastEnvelope {
			t.Fatalf("canonical content = %q want the latest completion envelope %q", canonical.Content, lastEnvelope)
		}
		if canonical.Content == enqueue {
			t.Fatalf("canonical content still the enqueue placeholder after %d patches", stepCount)
		}

		if lastStatus == api.WorkerSummaryStatusNeedsDecision {
			if canonical.WorkerSummary == nil || canonical.WorkerSummary.Status != api.WorkerSummaryStatusNeedsDecision {
				t.Fatalf("needs_decision must be a status on the canonical row, not a second card: %#v", canonical.WorkerSummary)
			}
		}

		preCount := len(all)
		preContent := canonical.Content
		failErr(t, "supersede", mgr.Runner.Transcript.SupersedeWorkerReport(ctx, sess.ID, canonicalID))
		after, err := store.GetMessages(ctx, sess.ID)
		failErr(t, "get messages post-supersede", err)
		if len(after) != preCount {
			t.Fatalf("row count changed across supersede: was %d now %d — no physical delete on retract", preCount, len(after))
		}
		var superseded *api.Message
		for i := range after {
			if after[i].ID == canonicalID {
				superseded = &after[i]
			}
		}
		if superseded == nil {
			t.Fatal("superseded row must remain in search (not deleted)")
			return
		}
		if superseded.Kind != api.MessageKindSuperseded {
			t.Fatalf("kind = %q want superseded — retract patches kind in place", superseded.Kind)
		}
		if superseded.Ord != canonicalOrd {
			t.Fatalf("ord mutated on supersede: was %d now %d — id/ord preserved on retract", canonicalOrd, superseded.Ord)
		}
		if superseded.Content != preContent {
			t.Fatalf("content mutated on supersede: was %q now %q — content preserved (row kept in search)", preContent, superseded.Content)
		}
	})
}
