package processcontrol

import (
	"context"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
	"time"
)

type completionFixture struct {
	captures []tools.SourceRunCapture
	wakes    []anchor.ID
	emitted  []anchor.ID
	waiting  bool
}

func (*completionFixture) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ID: "session"}, nil
}
func (f *completionFixture) RecordSourceRunTerminal(_ context.Context, _ *api.Session, _, _ string, c tools.SourceRunCapture, _ time.Time) {
	f.captures = append(f.captures, c)
}
func (*completionFixture) AfterTool(_ context.Context, _ *api.Session, _ string, _ map[string]any, out string, _ int, f guidance.ToolResultFacts) (string, guidance.ToolResultFacts) {
	return out + "\npolicy guidance", f
}
func (f *completionFixture) Emit(_ context.Context, _ string, id anchor.ID, _ anchor.Envelope) {
	f.emitted = append(f.emitted, id)
}
func (f *completionFixture) SessionSleepingOnProcess(_, handle string) bool {
	return f.waiting && handle == "handle"
}
func (f *completionFixture) NudgeProcessFinished(context.Context, string, string, anchor.Envelope) {
	f.wakes = append(f.wakes, anchor.ProcessFinished)
}
func (f *completionFixture) NudgeProcessRefused(context.Context, string, string, anchor.Envelope) {
	f.wakes = append(f.wakes, anchor.ProcessRefused)
}

func TestCommandSettlementRetainsReportAndWakesOnlySubscribedWait(t *testing.T) {
	f := &completionFixture{waiting: true}
	service := New(f, f, f, f)
	service.SetLoop(f, f)
	now := time.Now()
	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{SessionID: "session", Handle: "handle", OriginTool: "verify", RunID: "run", ToolCallID: "call", ExitCode: 1, FinishedAt: now, Tail: "failed check"})
	report, ok := service.Report("session", "handle")
	if !ok || !strings.Contains(report, "failed check") || !strings.Contains(report, "policy guidance") {
		t.Fatalf("retained report=%q found=%v", report, ok)
	}
	if len(f.wakes) != 1 || len(f.emitted) != 0 {
		t.Fatalf("wake=%v emit=%v", f.wakes, f.emitted)
	}
	f.waiting = false
	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{SessionID: "session", Handle: "another", OriginTool: "command", FinishedAt: now})
	if len(f.emitted) != 1 || len(f.wakes) != 1 {
		t.Fatalf("unsubscribed completion wake=%v emit=%v", f.wakes, f.emitted)
	}
	service.HandleCommandRefusal(t.Context(), bgprocess.RefusalNotice{SessionID: "session", Handle: "handle", OriginTool: "command", StartedAt: now, Unshown: 2})
	if len(f.emitted) != 2 {
		t.Fatalf("unsubscribed refusal=%v", f.emitted)
	}
	f.waiting = true
	service.HandleCommandRefusal(t.Context(), bgprocess.RefusalNotice{SessionID: "session", Handle: "handle", OriginTool: "command", StartedAt: now})
	if len(f.wakes) != 2 || f.wakes[1] != anchor.ProcessRefused {
		t.Fatalf("refusal wake=%v", f.wakes)
	}
	if _, found := service.Report("foreign", "handle"); found {
		t.Fatal("foreign session read completion report")
	}
}
