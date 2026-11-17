package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A streamed turn pairs the provider's reported input tokens with the host's
// transcript estimate, so compaction budgets follow the real receiver.
func TestStreamedTurnRecordsCompactionTokenObservation(t *testing.T) {
	mock := &usageMockLLM{MockProvider: llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	})}
	h := BuildForTest(t, WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	ctx := context.Background()
	// The session names the receiver the mock client answers as.
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{ProviderID: "mock", Model: "mock"}, t.TempDir())
	testutil.FailErr(t, "CreateHarnessSession", err)
	history := h.Sessions.Manager.Runner.History
	if history.Calibration(ctx, sess).Calibrated() {
		t.Fatal("session is calibrated before any turn")
	}

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "hello")
	testutil.FailErr(t, "Prompt", err)
	reloaded, err := h.Store.Get(h.OwnerCtx(t, ctx), sess.ID)
	testutil.FailErr(t, "reload session", err)
	cal := history.Calibration(ctx, reloaded)
	if cal.ReportedPromptTokens != 100 || cal.TranscriptEstimate <= 0 {
		t.Fatalf("calibration after a streamed turn = %+v, want the reported 100 prompt tokens and a transcript estimate", cal)
	}
}
