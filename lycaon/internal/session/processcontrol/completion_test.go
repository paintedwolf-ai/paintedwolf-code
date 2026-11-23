package processcontrol

import (
	"context"
	"encoding/json"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"strings"
	"testing"
	"time"
)

type completionFixture struct {
	captures  []tools.SourceRunCapture
	wakes     []anchor.ID
	emitted   []anchor.ID
	envelopes []anchor.Envelope
	dropped   []string
	waiting   bool
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
func (f *completionFixture) Emit(_ context.Context, _ string, id anchor.ID, env anchor.Envelope) {
	f.emitted = append(f.emitted, id)
	f.envelopes = append(f.envelopes, env)
}
func (f *completionFixture) DropSubject(_ string, _ anchor.ID, subject string) bool {
	kept := f.envelopes[:0]
	keptIDs := f.emitted[:0]
	dropped := false
	for i, env := range f.envelopes {
		if env.Subject == subject && f.emitted[i] == anchor.ProcessFinished {
			dropped = true
			continue
		}
		kept = append(kept, env)
		keptIDs = append(keptIDs, f.emitted[i])
	}
	f.envelopes = kept
	f.emitted = keptIDs
	if dropped {
		f.dropped = append(f.dropped, subject)
	}
	return dropped
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
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	service := New(f, f, f, f)
	service.SetLoop(f, f)
	now := time.Now()
	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{SessionID: "session", Handle: "handle", OriginTool: "verify", RunID: "run", ToolCallID: "call", ExitCode: 1, FinishedAt: now, Tail: "failed check",
		ProgramStop: bgprocess.ProgramStop{Code: "COMMAND_OPTION_UNSUPPORTED", Feedback: "refusal details"}})
	report, ok := service.Report("session", "handle")
	t.Logf("completion report for manual render inspection:\n%s", report)
	if !ok || !strings.Contains(report, "failed check") || !strings.Contains(report, "policy guidance") ||
		!strings.Contains(report, "program_stop_code=COMMAND_OPTION_UNSUPPORTED") {
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
	seen := &bgprocess.ExitObservation{}
	seen.Observe()
	seenCompletion := bgprocess.Completion{SessionID: "session", Handle: "seen", ExitObservation: seen, FinishedAt: now}
	service.HandleCommandCompletion(t.Context(), seenCompletion)
	if len(f.emitted) != 2 {
		t.Fatalf("observed exit emitted guidance: %v", f.emitted)
	}
	if _, found := service.Report("session", "seen"); !found {
		t.Fatal("observed exit lost the report for an explicit wait")
	}

	newExit := &bgprocess.ExitObservation{}
	seenCompletion.Handle, seenCompletion.ExitObservation = "fresh", newExit
	service.HandleCommandCompletion(t.Context(), seenCompletion)
	service.Observe("session", "fresh")
	if !newExit.Observed() || len(f.emitted) != 3 {
		t.Fatalf("retained report was not observed: delivered=%t signals=%v", newExit.Observed(), f.emitted)
	}
	if _, found := service.Report("foreign", "handle"); found {
		t.Fatal("foreign session read completion report")
	}
}

func TestBackgroundPreviewPreservesUTF8AndMostRecentOutput(t *testing.T) {
	output := &api.BackgroundProcessOutput{Chunks: []api.BackgroundProcessChunk{{Text: "old"}, {Text: "aéz"}}}
	capBackgroundOutput(output, 3)
	if !output.Truncated || len(output.Chunks) != 1 || output.Chunks[0].Text != "éz" {
		t.Fatalf("bounded screened output=%+v", output)
	}
}

func TestSilentRunningProcessSerializesEmptyChunksAcrossEndpoints(t *testing.T) {
	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	svc := &Service{Background: bg, Held: heldcall.New(nil, nil)}
	handle, err := bg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "s", ProjectID: "p", Mode: bgprocess.JobModeBackground, Runner: hostcmd.NewRunner(),
		Request: hostcmd.Request{
			Launch: exec.HostLaunch("silent process test"), ProjectDir: t.TempDir(),
			Stages: []exec.Stage{{Name: "sh", Args: []string{"-c", "sleep 30"}}},
		},
	})
	if err != nil {
		t.Fatalf("start silent process: %v", err)
	}
	t.Cleanup(func() { stopAndWait(t, bg, handle, bgprocess.TerminationStoppedByUser) })

	directOut, err := svc.GetBackgroundProcessOutput(t.Context(), "s", handle)
	if err != nil {
		t.Fatalf("get output: %v", err)
	}
	if directOut.Chunks == nil || len(directOut.Chunks) != 0 {
		t.Fatalf("direct output chunks = %#v", directOut.Chunks)
	}
	directJSON, err := json.Marshal(directOut)
	if err != nil || !strings.Contains(string(directJSON), `"chunks":[]`) {
		t.Fatalf("direct output json = %s (err=%v)", directJSON, err)
	}

	listed := svc.ListBackgroundProcesses(t.Context(), "s")
	if len(listed) != 1 || listed[0].Output == nil || listed[0].Output.Chunks == nil {
		t.Fatalf("listed processes = %+v", listed)
	}
	listJSON, err := json.Marshal(api.BackgroundProcessListResponse{Processes: listed})
	if err != nil || !strings.Contains(string(listJSON), `"chunks":[]`) {
		t.Fatalf("list json = %s (err=%v)", listJSON, err)
	}
	bootstrapJSON, err := json.Marshal([]api.BackgroundProcessOutput{*listed[0].Output})
	if err != nil || !strings.Contains(string(bootstrapJSON), `"chunks":[]`) {
		t.Fatalf("bootstrap background_outputs json = %s (err=%v)", bootstrapJSON, err)
	}
}

func TestHandleSubjectResolvesHeldAndUnknownHandles(t *testing.T) {
	var nilSvc *Service
	if got := nilSvc.HandleSubject("s", "h"); got != "" {
		t.Fatalf("nil service = %q", got)
	}
	held := heldcall.New(nil, nil)
	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	svc := &Service{Background: bg, Held: held}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	bgHandle, err := bg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: "s", ProjectID: "p", Mode: bgprocess.JobModeBackground, Runner: hostcmd.NewRunner(),
		Request: hostcmd.Request{Launch: exec.HostLaunch("processcontrol test"), ProjectDir: t.TempDir(), Stages: []exec.Stage{{Name: exe, Args: []string{"-test.list=^$"}}}},
	})
	if err != nil {
		t.Fatalf("start bg process: %v", err)
	}
	t.Cleanup(func() { stopAndWait(t, bg, bgHandle, bgprocess.TerminationStoppedByAgent) })
	if got := svc.HandleSubject("s", bgHandle); !strings.Contains(got, "-test.list=^$") {
		t.Fatalf("bg subject = %q", got)
	}
	release := make(chan struct{})
	defer close(release)
	outcome, err := held.Run(t.Context(), heldcall.Spec{
		SessionID: "s", Tool: "fetch_url", ToolCallID: "c1", Budget: 10 * time.Millisecond, DisplayTitle: "fetch url · https://example.com",
	}, func(ctx context.Context) heldcall.Settled {
		<-release
		return heldcall.Settled{Outcome: api.ToolResultOutcomeCompleted}
	})
	if err != nil || outcome.Handle == "" {
		t.Fatalf("start held call: %v %+v", err, outcome)
	}
	if got := svc.HandleSubject("s", outcome.Handle); got != "fetch url · https://example.com" {
		t.Fatalf("held subject = %q", got)
	}
	fallbackOut, err := held.Run(t.Context(), heldcall.Spec{
		SessionID: "s", Tool: "survey_repo", ToolCallID: "c2", Budget: 10 * time.Millisecond,
	}, func(ctx context.Context) heldcall.Settled {
		<-release
		return heldcall.Settled{Outcome: api.ToolResultOutcomeCompleted}
	})
	if err != nil || fallbackOut.Handle == "" {
		t.Fatalf("start fallback held call: %v %+v", err, fallbackOut)
	}
	if got := svc.HandleSubject("s", fallbackOut.Handle); got != "survey repo" {
		t.Fatalf("fallback held subject = %q", got)
	}
	if got := svc.HandleSubject("other", outcome.Handle); got != "" {
		t.Fatalf("foreign session subject = %q", got)
	}
}

func TestTwoUserStoppedCommandsBetweenTurnsQueueSeparatelyWithCommandLinesAndReason(t *testing.T) {
	f := &completionFixture{}
	service := New(f, f, f, f)
	service.SetLoop(f, f)
	now := time.Now()

	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:         "session",
		Handle:            "bg-1",
		OriginTool:        "command",
		TerminationReason: bgprocess.TerminationStoppedByUser,
		ExitCode:          -1,
		FinishedAt:        now,
		Stages:            []hostcmd.StageResult{{Command: "npm run dev"}},
	})
	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:         "session",
		Handle:            "bg-2",
		OriginTool:        "command",
		TerminationReason: bgprocess.TerminationStoppedByUser,
		ExitCode:          -1,
		FinishedAt:        now,
		Stages:            []hostcmd.StageResult{{Command: "tail -f app.log"}},
	})

	if len(f.envelopes) != 2 {
		t.Fatalf("emitted envelopes = %d, want 2", len(f.envelopes))
	}
	if f.envelopes[0].Subject != "bg-1" || !strings.Contains(f.envelopes[0].CommandCompletionDigest, "termination=stopped_by_user") || !strings.Contains(f.envelopes[0].CommandCompletionDigest, "command=npm run dev") {
		t.Fatalf("first envelope = %+v", f.envelopes[0])
	}
	if f.envelopes[1].Subject != "bg-2" || !strings.Contains(f.envelopes[1].CommandCompletionDigest, "termination=stopped_by_user") || !strings.Contains(f.envelopes[1].CommandCompletionDigest, "command=tail -f app.log") {
		t.Fatalf("second envelope = %+v", f.envelopes[1])
	}
}

func TestCompletionDeliveryRespectsTerminationReasonRouting(t *testing.T) {
	f := &completionFixture{}
	service := New(f, f, f, f)
	service.SetLoop(f, f)
	now := time.Now()

	for _, teardownReason := range []bgprocess.TerminationReason{
		bgprocess.TerminationChatDeleted,
		bgprocess.TerminationProjectDeleted,
		bgprocess.TerminationAppQuit,
		bgprocess.TerminationWorkerCompleted,
		bgprocess.TerminationWorkerCancelled,
	} {
		service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
			SessionID:         "session",
			Handle:            "h-" + string(teardownReason),
			OriginTool:        "command",
			TerminationReason: teardownReason,
			FinishedAt:        now,
		})
	}
	if len(f.emitted) != 0 || len(f.wakes) != 0 {
		t.Fatalf("teardown completions delivered to session: emitted=%v wakes=%v", f.emitted, f.wakes)
	}

	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:         "session",
		Handle:            "h-agent",
		OriginTool:        "command",
		TerminationReason: bgprocess.TerminationStoppedByAgent,
		FinishedAt:        now,
	})
	if len(f.emitted) != 0 {
		t.Fatalf("stopped_by_agent emitted unrequested completion kick: %v", f.emitted)
	}

	for _, liveReason := range []bgprocess.TerminationReason{
		bgprocess.TerminationExited,
		bgprocess.TerminationTimedOut,
		bgprocess.TerminationStoppedByUser,
		bgprocess.TerminationRootDetached,
	} {
		service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
			SessionID:         "session",
			Handle:            "h-" + string(liveReason),
			OriginTool:        "command",
			TerminationReason: liveReason,
			FinishedAt:        now,
		})
	}
	if len(f.emitted) != 4 {
		t.Fatalf("live session completions emitted = %d, want 4", len(f.emitted))
	}
}

// The digest's elapsed is run time; time awaiting approval is reported apart.
func TestCommandCompletionDigestSeparatesApprovalWaitFromRunTime(t *testing.T) {
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	completion := bgprocess.Completion{Handle: "bg-1", OriginTool: "command", StartedAt: started, FinishedAt: started.Add(21750 * time.Millisecond), PersonWait: 13290 * time.Millisecond}
	digest := CommandCompletionDigest(completion)
	if !strings.Contains(digest, " elapsed=8.46s ") || !strings.Contains(digest, " awaited_approval=13.29s") {
		t.Fatalf("digest = %q", digest)
	}
	completion.PersonWait = 0
	if digest := CommandCompletionDigest(completion); !strings.Contains(digest, " elapsed=21.75s ") || strings.Contains(digest, "awaited_approval") {
		t.Fatalf("digest without a wait = %q", digest)
	}
}

// stopAndWait stops a test's background command and waits until it and its
// supervisor have exited, so no process outlives the test that started it.
func stopAndWait(t *testing.T, bg *bgprocess.Registry, handle string, reason bgprocess.TerminationReason) {
	t.Helper()
	_, _ = bg.Lifecycle.Stop("s", handle, reason)
	if _, _, err := bg.Lifecycle.Wait(context.Background(), "s", handle); err != nil {
		t.Errorf("wait for background command %s: %v", handle, err)
	}
}

func TestCommandCompletionPresentationClaimsAcrossWaitWatchAndKick(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	clock := watch.NewFakeClock(now)
	watches := watch.NewStore(database, clock)
	owner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "session"}

	f := &completionFixture{}
	service := New(f, f, f, f)
	service.SetLoop(f, f)
	service.SetWatches(watches)

	// Explicit watch claims completion without enqueueing a fallback kick, while recording verification.
	rec1, err := watches.Register(t.Context(), watch.RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: watch.ConditionProcessDone, Handles: []string{"cmd-watched"}}},
	})
	testutil.FailErr(t, "register watch", err)

	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:  "session",
		Handle:     "cmd-watched",
		OriginTool: "verify",
		RunID:      "occ-watched",
		ToolCallID: "call-1",
		Stages:     []hostcmd.Stage{{Command: "go test ./..."}},
		FinishedAt: now,
	})
	if len(f.captures) != 1 {
		t.Fatalf("verification captures = %d, want 1", len(f.captures))
	}
	if _, ok := service.Report("session", "cmd-watched"); !ok {
		t.Fatal("expected completion report published before presentation routing")
	}
	if len(f.emitted) != 0 || len(f.wakes) != 0 {
		t.Fatalf("explicit watch completion leaked kick=%v wake=%v", f.emitted, f.wakes)
	}
	view1, err := watches.Inspect(t.Context(), owner, rec1.Handle)
	testutil.FailErr(t, "inspect watched completion", err)
	if view1.PendingMatches != 1 {
		t.Fatalf("pending watch matches = %d, want 1", view1.PendingMatches)
	}

	// Uncovered completion queues one fallback kick; a later watch claim transfers and drops that queued kick.
	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:  "session",
		Handle:     "cmd-transfer",
		OriginTool: "command",
		RunID:      "occ-transfer",
		Stages:     []hostcmd.Stage{{Command: "echo transfer"}},
		FinishedAt: now,
	})
	if len(f.emitted) != 1 || len(f.captures) != 2 {
		t.Fatalf("fallback kick emitted=%d captures=%d, want 1 and 2", len(f.emitted), len(f.captures))
	}
	_, err = watches.Register(t.Context(), watch.RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: watch.ConditionProcessDone, Handles: []string{"cmd-transfer"}}},
	})
	testutil.FailErr(t, "register transfer watch", err)
	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:  "session",
		Handle:     "cmd-transfer",
		OriginTool: "command",
		RunID:      "occ-transfer",
		FinishedAt: now,
	})
	if len(f.emitted) != 0 || len(f.dropped) != 1 || f.dropped[0] != "cmd-transfer" {
		t.Fatalf("expected queued kick dropped on watch claim: emitted=%v dropped=%v", f.emitted, f.dropped)
	}

	// An already-admitted kick supplies its receipt ID without leaving a second pending notification.
	_, err = watch.ClaimPresentation(t.Context(), watches, owner, "occ-admitted", watch.ClaimFallbackKick, true, "msg-kick-receipt-1")
	testutil.FailErr(t, "seed admitted kick", err)
	recAdmitted, err := watches.Register(t.Context(), watch.RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: watch.ConditionProcessDone, Handles: []string{"cmd-admitted"}}},
	})
	testutil.FailErr(t, "register post-admission watch", err)
	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:  "session",
		Handle:     "cmd-admitted",
		OriginTool: "command",
		RunID:      "occ-admitted",
		FinishedAt: now,
	})
	viewAdmitted, err := watches.Inspect(t.Context(), owner, recAdmitted.Handle)
	testutil.FailErr(t, "inspect post-admission watch", err)
	if viewAdmitted.PendingMatches != 0 || len(viewAdmitted.RetainedRefs) != 1 || viewAdmitted.RetainedRefs[0] != "msg-kick-receipt-1" {
		t.Fatalf("expected admitted receipt attached with 0 pending matches: %+v", viewAdmitted)
	}
}
