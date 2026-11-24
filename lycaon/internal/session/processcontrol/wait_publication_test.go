package processcontrol

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHandlesRunningHoldsUntilTerminalReportPublishesAndEmitsValueFreePhase(t *testing.T) {
	f := &completionFixture{}
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	service := New(f, f, f, f)
	service.Background = bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	service.Held = heldcall.New(nil, nil)
	t.Cleanup(func() { _ = service.Background.Lifecycle.Close(context.Background()) })

	var mu sync.Mutex
	var phases []awaitstore.PhaseRecord
	unsub := awaitstore.RegisterPhaseObserver(func(rec awaitstore.PhaseRecord) {
		mu.Lock()
		phases = append(phases, rec)
		mu.Unlock()
	})
	defer unsub()

	const secretOutput = "private-stdout-payload-7711"
	handle, err := service.Background.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID: "session",
		Run: func(_ context.Context, out, _ io.Writer) (bgprocess.ProgramJobResult, error) {
			_, _ = io.WriteString(out, secretOutput)
			return ProgramJobResultZero(), nil
		},
	})
	testutil.FailErr(t, "start program", err)
	_, _, err = service.Background.Lifecycle.Wait(t.Context(), "session", handle)
	testutil.FailErr(t, "wait program exit", err)

	// Process exited, but HandleCommandCompletion has not published its report yet.
	if !service.HandlesRunning("session", nil) || !service.HandlesRunning("session", []string{handle}) {
		t.Fatal("finished job with unpublished report allowed cycle to close prematurely")
	}
	if _, ok := service.ReportPublishedAt("session", handle); ok {
		t.Fatal("report marked published before HandleCommandCompletion")
	}

	finishedAt := time.Now().UTC().Add(-10 * time.Millisecond)
	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:         "session",
		Handle:            handle,
		OriginTool:        "command",
		ToolCallID:        "call-34",
		TerminationReason: bgprocess.TerminationExited,
		FinishedAt:        finishedAt,
		Tail:              secretOutput,
	})
	pubAt, ok := service.ReportPublishedAt("session", handle)
	if !ok || pubAt.Before(finishedAt) {
		t.Fatalf("publishedAt = %v (ok=%v), finishedAt = %v", pubAt, ok, finishedAt)
	}
	if service.HandlesRunning("session", nil) {
		t.Fatal("HandlesRunning stayed true after terminal report published")
	}

	// Held call settlement also records publication phase.
	outcome, err := service.Held.Run(t.Context(), heldcall.Spec{
		SessionID: "session", Tool: "grep", Budget: time.Millisecond,
	}, func(ctx context.Context) heldcall.Settled {
		time.Sleep(5 * time.Millisecond)
		return heldcall.Settled{Outcome: api.ToolResultOutcomeCompleted, Content: secretOutput}
	})
	testutil.FailErr(t, "run held call", err)
	if outcome.Handle != "" {
		testutil.WaitFor(t, "held call settles", func() bool {
			st, sErr := service.Held.Status("session", outcome.Handle)
			return sErr == nil && st.Settled != nil
		})
		service.HandleHeldCallSettled("session", outcome.Handle)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(phases) == 0 {
		t.Fatal("expected process_report_published phase records")
	}
	raw, mErr := json.Marshal(phases)
	testutil.FailErr(t, "marshal phases", mErr)
	if strings.Contains(string(raw), secretOutput) {
		t.Fatalf("phase record leaked command output: %s", string(raw))
	}
}

func ProgramJobResultZero() bgprocess.ProgramJobResult { return bgprocess.ProgramJobResult{ExitCode: 0} }
