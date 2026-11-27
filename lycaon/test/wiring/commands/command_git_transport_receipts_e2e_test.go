//go:build unix

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sshproxy"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type coordinatorLoopEnabledLimits struct{}

func (coordinatorLoopEnabledLimits) SessionLimits(string) settings.SessionLimits {
	limits := settings.DefaultSessionLimits()
	enabled := true
	limits.CoordinatorLoop = &enabled
	return limits
}

type continuedTransportClient struct {
	h              *wiring.Harness
	sessionID      atomic.Value
	step           atomic.Int32
	observedHandle atomic.Value
}

func (c *continuedTransportClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	hasWait, hasCmdOut := false, false
	for _, td := range req.Tools {
		switch td.Name {
		case "wait":
			hasWait = true
		case "command_output":
			hasCmdOut = true
		}
	}
	if !hasWait {
		return &modelcall.Completion{ToolCalls: []wire.ToolCall{{
			ID: "load-wait", Name: "request_tools", Args: map[string]any{"need": "wait"},
		}}}, nil
	}
	if !hasCmdOut {
		return &modelcall.Completion{ToolCalls: []wire.ToolCall{{
			ID: "load-cmd-out", Name: "request_tools", Args: map[string]any{"need": "command_output"},
		}}}, nil
	}
	switch c.step.Add(1) {
	case 1:
		return &modelcall.Completion{ToolCalls: []wire.ToolCall{{
			ID: "cont-cmd-1", Name: "command", Args: map[string]any{"command": "external -- cat gate.fifo; git ls-remote origin"},
		}}}, nil
	case 2:
		sid, _ := c.sessionID.Load().(string)
		var running command.RunningProgramResult
		if msgs, err := c.h.Store.Transcript.GetMessages(ctx, sid); err == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				m := messageview.RedactMessage(msgs[i])
				if m.Role == wire.MessageRoleTool && m.ToolResult != nil && m.ToolResult.ToolCallID == "cont-cmd-1" {
					_, body, _, ok := hostmarker.SplitToolJSONBody(msgs[i].ToolResult.Content)
					if !ok {
						body = msgs[i].ToolResult.Content
					}
					_ = json.Unmarshal([]byte(body), &running)
					break
				}
			}
		}
		c.observedHandle.Store(running.Handle)
		return &modelcall.Completion{ToolCalls: []wire.ToolCall{{
			ID: "wait-before-exit", Name: "wait",
			Args: map[string]any{
				"timeout_ms": 60_000,
				"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{running.Handle}}},
			},
		}}}, nil
	case 3:
		return &modelcall.Completion{Content: "Observed continued wake."}, nil
	case 4:
		hdl, _ := c.observedHandle.Load().(string)
		return &modelcall.Completion{ToolCalls: []wire.ToolCall{{
			ID: "wait-after-exit", Name: "wait",
			Args: map[string]any{
				"timeout_ms": 60_000,
				"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{hdl}}},
			},
		}}}, nil
	case 5:
		hdl, _ := c.observedHandle.Load().(string)
		return &modelcall.Completion{ToolCalls: []wire.ToolCall{{
			ID: "wait-duplicate", Name: "wait",
			Args: map[string]any{
				"timeout_ms": 60_000,
				"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{hdl}}},
			},
		}}}, nil
	case 6:
		hdl, _ := c.observedHandle.Load().(string)
		return &modelcall.Completion{ToolCalls: []wire.ToolCall{{
			ID: "cmd-out-after", Name: "command_output",
			Args: map[string]any{"handle": hdl, "cursor": 0},
		}}}, nil
	default:
		return &modelcall.Completion{Content: "Post-exit checks finished."}, nil
	}
}

func (c *continuedTransportClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	comp, err := c.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	if len(comp.ToolCalls) > 0 {
		ch <- modelcall.StreamChunk{ToolCalls: comp.ToolCalls, Done: true}
	} else {
		ch <- modelcall.StreamChunk{Content: comp.Content, Done: true}
	}
	close(ch)
	return ch, nil
}

type receiptDocumentRow struct {
	Type      string                       `json:"type"`
	ProgramID string                       `json:"program_id"`
	Execution wire.CommandExecutionReceipt `json:"execution"`
}

func TestGitSSHTransportDeliveryParityAndReceiptReopen(t *testing.T) {
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")

	agentSock, userPub := startSigningSSHAgent(t)
	t.Setenv("SSH_AUTH_SOCK", agentSock)

	bareDir, _, _ := createBareSeededRepo(t)
	sshFixture := startSSHGitServer(t, bareDir, userPub)
	deniedURL := fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", sshFixture.port)
	wantDest := fmt.Sprintf("127.0.0.1:%d", sshFixture.port)

	hostDir := t.TempDir()
	var (
		immSession, immProgram     string
		parkSession, parkProgramID string
		contSession, contProgramID string
		contHandle                 string
	)

	t.Run("first boot", func(t *testing.T) {
		models := newSessionModels()
		clock := programtest.NewManualClock()
		h := wiring.BuildForTestAt(t, hostDir, wiring.WithLLMClient(models), wiring.WithProgramClock(clock))
		h.Sessions.Manager.Limits.SetProvider(coordinatorLoopEnabledLimits{})
		p := &programHost{h: h, models: models}

		promptBounded := func(t *testing.T, sessionID, text string) {
			t.Helper()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			_, err := h.Sessions.Manager.Submissions.Prompt(ctx, sessionID, text)
			testutil.FailErr(t, "prompt "+text, err)
		}

		seedRepo := func(t *testing.T) string {
			t.Helper()
			dir := newSeededGitWorkDir(t)
			gittest.Run(t, dir, "remote", "add", "origin", deniedURL)
			return dir
		}

		// 1. Immediate completion.
		immRepo := seedRepo(t)
		imm := p.withClientAt(t, immRepo, programModel(map[string]any{"command": "git ls-remote origin"}), wire.CreateSessionRequest{}, "")
		imm.h.SeedProgress(t, t.Context(), imm.session)
		testutil.FailErr(t, "orient imm", h.Workflows.Manager.Fanout.RecordBoardOrientReady(t.Context(), imm.session, "orient-imm"))
		promptBounded(t, imm.session, "run the program")
		immSession = imm.session

		immRaw := rawToolResult(t, h, imm.session, "program-1")
		assertRenderedTransportDenied(t, "immediate", immRaw, wantDest)
		immOutcome := decodeProgramOutcomeFromToolResult(t, immRaw)
		immProgram = immOutcome.ProgramID
		if immOutcome.Status != 128 || len(immOutcome.NonzeroExecutions) != 1 || immOutcome.NonzeroExecutions[0].EffectCertainty != "none" {
			t.Fatalf("immediate outcome = %+v", immOutcome)
		}

		// 2. Park then wake (`sleep 120; git ls-remote origin`).
		const parkSleep = 120 * time.Second
		parkRepo := seedRepo(t)
		var parkStep atomic.Int32
		parkClient := llmtest.NewKickDrivenMock(llmtest.KickDrivenConfig{
			Fallback: func(_ context.Context, _ llmtest.Snapshot) modelcall.Completion {
				if parkStep.Add(1) == 1 {
					return modelcall.Completion{ToolCalls: []wire.ToolCall{{
						ID: "park-cmd-1", Name: "command", Args: map[string]any{"command": "sleep 120; git ls-remote origin"},
					}}}
				}
				return modelcall.Completion{Content: "Parked program settled."}
			},
		})
		park := p.withClientAt(t, parkRepo, parkClient, wire.CreateSessionRequest{}, "")
		park.h.SeedProgress(t, t.Context(), park.session)
		testutil.FailErr(t, "orient park", h.Workflows.Manager.Fanout.RecordBoardOrientReady(t.Context(), park.session, "orient-park"))
		parkSession = park.session

		promptBounded(t, park.session, "run the program")
		parkedRaw := rawToolResult(t, h, park.session, "park-cmd-1")
		_, parkedJSON, _, ok := hostmarker.SplitToolJSONBody(parkedRaw)
		if !ok {
			parkedJSON = parkedRaw
		}
		var parked command.ParkedProgramResult
		testutil.FailErr(t, "decode parked program", json.Unmarshal([]byte(parkedJSON), &parked))
		if parked.State != "parked" || parked.ProgramID == "" || parked.LeaseID == "" {
			t.Fatalf("expected parked program result, got %+v (%s)", parked, parkedRaw)
		}
		parkProgramID = parked.ProgramID

		testutil.WaitFor(t, "sleep 120 timer registered on manual clock", func() bool {
			return slices.Contains(clock.TimersDue(), parkSleep)
		})
		clock.Advance(parkSleep)

		var wakeMsg wire.Message
		testutil.WaitFor(t, "parked program wakes and settles", func() bool {
			msgs, err := h.Store.Transcript.GetMessages(t.Context(), park.session)
			if err != nil {
				return false
			}
			for _, m := range msgs {
				if m.Kind == wire.MessageKindHostLoopWake && strings.Contains(m.Content, parkProgramID) {
					wakeMsg = m
					return true
				}
			}
			return false
		})
		wiring.WaitForSessionIdleGate(t, h, t.Context(), park.session)
		assertRenderedTransportDenied(t, "park-wake", wakeMsg.Content, wantDest)
		parkWakeOutcome := decodeProgramOutcomeFromWake(t, wakeMsg.Content)
		if parkWakeOutcome.ProgramID != parkProgramID || parkWakeOutcome.Status != 128 || len(parkWakeOutcome.NonzeroExecutions) != 1 || parkWakeOutcome.NonzeroExecutions[0].EffectCertainty != "none" {
			t.Fatalf("parked wake outcome = %+v", parkWakeOutcome)
		}

		// 3. Continued background program: wait before exit, wait after exit, duplicate wait, command_output.
		contRepo := seedRepo(t)
		fifoPath := filepath.Join(contRepo, "gate.fifo")
		testutil.FailErr(t, "mkfifo", syscall.Mkfifo(fifoPath, 0o600))
		gateOpened := make(chan struct{})
		releaseDial := make(chan struct{})
		var releaseOnce sync.Once
		releaseGate := func() { releaseOnce.Do(func() { close(releaseDial) }) }
		t.Cleanup(func() {
			releaseGate()
			if r, err := os.OpenFile(fifoPath, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = r.Close()
			}
		})
		go func() {
			w, err := os.OpenFile(fifoPath, os.O_WRONLY, 0)
			close(gateOpened)
			if err == nil {
				<-releaseDial
				_ = w.Close()
			}
		}()

		contClient := &continuedTransportClient{h: h}
		cont := p.withClientAt(t, contRepo, contClient, wire.CreateSessionRequest{}, "")
		cont.h.SeedProgress(t, t.Context(), cont.session)
		testutil.FailErr(t, "orient cont", h.Workflows.Manager.Fanout.RecordBoardOrientReady(t.Context(), cont.session, "orient-cont"))
		contSession = cont.session
		contClient.sessionID.Store(cont.session)

		contCtx, cancelCont := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancelCont()
		stopApprover := make(chan struct{})
		defer close(stopApprover)
		go func() {
			kind := wire.CheckpointKindToolApproval
			approved := map[string]bool{}
			ticker := time.NewTicker(15 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopApprover:
					return
				case <-contCtx.Done():
					return
				case <-ticker.C:
					pending, err := h.Sessions.Checkpoints.ListPending(contCtx, cont.session, &kind)
					if err != nil {
						continue
					}
					for _, cp := range pending {
						if approved[cp.ID] || cp.ToolApproval == nil {
							continue
						}
						approved[cp.ID] = true
						_, _ = h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(
							h.OwnerCtx(t, contCtx), cont.session, cp.ID, cp.ToolApproval.Plan.RecommendedOptionID,
						)
					}
				}
			}
		}()

		promptDone := make(chan error, 1)
		go func() {
			_, err := h.Sessions.Manager.Submissions.Prompt(contCtx, cont.session, "run the program")
			promptDone <- err
		}()

		select {
		case <-gateOpened:
		case err := <-promptDone:
			releaseGate()
			t.Fatalf("continued prompt exited before opening gate fifo: %v", err)
		case <-time.After(15 * time.Second):
			releaseGate()
			t.Fatal("continued program never opened gate fifo")
		}
		testutil.WaitFor(t, "foreground inline wait timer registered", func() bool {
			return slices.Contains(clock.TimersDue(), toolcontract.ProgramInlineWait)
		})
		clock.Advance(toolcontract.ProgramInlineWait)
		select {
		case err := <-promptDone:
			testutil.FailErr(t, "continued turn parks on wait", err)
		case <-time.After(15 * time.Second):
			releaseGate()
			t.Fatal("continued turn did not park on wait within 15s")
		}

		runningRaw := rawToolResult(t, h, cont.session, "cont-cmd-1")
		_, runningJSON, _, ok := hostmarker.SplitToolJSONBody(runningRaw)
		if !ok {
			runningJSON = runningRaw
		}
		var running command.RunningProgramResult
		testutil.FailErr(t, "decode running program", json.Unmarshal([]byte(runningJSON), &running))
		if running.State != "running" || running.ProgramID == "" || running.Handle == "" {
			t.Fatalf("expected running program result, got %+v", running)
		}
		contProgramID = running.ProgramID
		contHandle = running.Handle

		loop := h.CoordinatorRuntime.CoordinatorLoop()
		testutil.WaitFor(t, "session sleeping on process_done handle", func() bool {
			return slices.Contains(loop.Waits.ActiveProcessHandles(cont.session), contHandle)
		})

		releaseGate()

		var contWake wire.Message
		testutil.WaitFor(t, "wait-before-exit wakes on process completion", func() bool {
			msgs, err := h.Store.Transcript.GetMessages(t.Context(), cont.session)
			if err != nil {
				return false
			}
			for _, m := range msgs {
				if m.Kind == wire.MessageKindHostLoopWake && strings.Contains(m.Content, contHandle) {
					contWake = m
					return true
				}
			}
			return false
		})
		wiring.WaitForSessionIdleGate(t, h, t.Context(), cont.session)
		assertRenderedTransportDenied(t, "wait-before-exit-wake", contWake.Content, wantDest)
		for _, wantToken := range []string{"exit_code=128", "program_id=" + contProgramID, "effect_certainty=none", "stderr_ref=output:"} {
			if !strings.Contains(contWake.Content, wantToken) {
				t.Fatalf("continued wake missing %q in %q", wantToken, contWake.Content)
			}
		}

		// Run wait after exit, duplicate wait, and command_output in a follow-up prompt.
		promptBounded(t, cont.session, "verify post-exit reads")
		wiring.WaitForSessionIdleGate(t, h, t.Context(), cont.session)

		waitAfterRaw := rawToolResult(t, h, cont.session, "wait-after-exit")
		waitDupRaw := rawToolResult(t, h, cont.session, "wait-duplicate")
		waitAfterReport := decodeWaitReport(t, waitAfterRaw)
		waitDupReport := decodeWaitReport(t, waitDupRaw)
		if waitAfterReport != waitDupReport {
			t.Fatalf("wait-after-exit and duplicate wait reports diverged:\nfirst: %s\ndup:   %s", waitAfterReport, waitDupReport)
		}
		assertRenderedTransportDenied(t, "wait-after-exit", waitAfterReport, wantDest)
		for _, wantToken := range []string{"exit_code=128", "program_id=" + contProgramID, "effect_certainty=none", "stderr_ref=output:"} {
			if !strings.Contains(waitAfterReport, wantToken) {
				t.Fatalf("wait-after-exit report missing %q in %q", wantToken, waitAfterReport)
			}
		}

		cmdOutRaw := rawToolResult(t, h, cont.session, "cmd-out-after")
		assertRenderedTransportDenied(t, "command_output", cmdOutRaw, wantDest)

		// Assert exactly one terminal wake delivery for the continued handle.
		msgs, err := h.Store.Transcript.GetMessages(t.Context(), cont.session)
		testutil.FailErr(t, "read continued session transcript", err)
		wakeCount := 0
		for _, m := range msgs {
			if m.Kind == wire.MessageKindHostLoopWake && strings.Contains(m.Content, contHandle) {
				wakeCount++
			}
		}
		if wakeCount != 1 {
			t.Fatalf("expected exactly 1 terminal wake delivery for %s, got %d", contHandle, wakeCount)
		}
	})

	// 4. Reopen store after full host restart and compare durable receipts across all 3 programs.
	reopened := wiring.BuildForTestAt(t, hostDir)
	recorder := invocation.NewSQLRecorder(reopened.DB)

	type programCase struct {
		label     string
		sessionID string
		programID string
	}
	cases := []programCase{
		{label: "immediate", sessionID: immSession, programID: immProgram},
		{label: "park-wake", sessionID: parkSession, programID: parkProgramID},
		{label: "continued", sessionID: contSession, programID: contProgramID},
	}
	for _, tc := range cases {
		rawReceipt, err := reopened.Commands.ProgramReceiptResult(t.Context(), tc.sessionID, tc.programID)
		testutil.FailErr(t, tc.label+" ProgramReceiptResult", err)
		var receiptOutcome command.ProgramOutcome
		testutil.FailErr(t, tc.label+" decode receipt outcome", json.Unmarshal([]byte(rawReceipt), &receiptOutcome))
		if receiptOutcome.ProgramID != tc.programID || receiptOutcome.Status != 128 || len(receiptOutcome.NonzeroExecutions) != 1 || receiptOutcome.NonzeroExecutions[0].EffectCertainty != "none" {
			t.Fatalf("%s reopened ProgramReceiptResult = %+v", tc.label, receiptOutcome)
		}

		page, err := recorder.ReadProgramPage(t.Context(), tc.sessionID, tc.programID, "", 10)
		testutil.FailErr(t, tc.label+" ReadProgramPage", err)
		var gitExec *wire.CommandExecutionReceipt
		for i := range page.Executions {
			if page.Executions[i].NativeCommand == "git" || strings.HasPrefix(page.Executions[i].Command, "git ") {
				gitExec = &page.Executions[i]
				break
			}
		}
		if gitExec == nil {
			t.Fatalf("%s missing git execution receipt in %+v", tc.label, page.Executions)
		}
		if gitExec.StatusCode == nil || *gitExec.StatusCode != 128 || gitExec.EffectCertainty != "none" || gitExec.DetailRef == "" {
			t.Fatalf("%s execution exit/refs mismatch: %+v", tc.label, *gitExec)
		}
		tr := gitExec.Transport
		if tr == nil || tr.Cause != "endpoint_denied" || tr.Stage != "policy" || tr.Destination != wantDest || tr.PolicyReason != "loopback_port_ungranted" || len(tr.Attempts) != 1 || tr.Attempts[0].ReplyCode != 2 {
			t.Fatalf("%s execution transport mismatch: %+v", tc.label, tr)
		}

		// Files receipt HTTP document endpoint parity after restart.
		req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+tc.sessionID+"/program-receipt-document?scopes="+tc.programID, nil)
		req.Header.Set("Authorization", "Bearer "+reopened.APIToken)
		rec := httptest.NewRecorder()
		reopened.Server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s receipt document HTTP status=%d body=%s", tc.label, rec.Code, rec.Body.String())
		}
		var doc wire.CommandReceiptDocumentPage
		testutil.FailErr(t, tc.label+" decode document page", json.Unmarshal(rec.Body.Bytes(), &doc))
		if !doc.Settled {
			t.Fatalf("%s receipt document not settled: %+v", tc.label, doc)
		}
		foundExec := false
		for _, row := range doc.Rows {
			var parsed receiptDocumentRow
			testutil.FailErr(t, tc.label+" decode row", json.Unmarshal([]byte(row.Text), &parsed))
			if parsed.Type == "execution" && parsed.Execution.ID == gitExec.ID {
				foundExec = true
				if parsed.Execution.Transport == nil || parsed.Execution.Transport.Cause != "endpoint_denied" || parsed.Execution.Transport.Destination != wantDest || parsed.Execution.Transport.PolicyReason != "loopback_port_ungranted" {
					t.Fatalf("%s Files document execution transport = %+v", tc.label, parsed.Execution.Transport)
				}
			}
		}
		if !foundExec {
			t.Fatalf("%s Files document missing execution %s", tc.label, gitExec.ID)
		}
	}
}

func TestGitSSHTransportSetupDialUnknownAndMultiRemoteIsolation(t *testing.T) {
	var (
		sshHookMu sync.RWMutex
		sshHook   func(context.Context, sshproxy.DialFunc) (*gitexec.AgentSSH, error)
	)
	setSSHHook := func(fn func(context.Context, sshproxy.DialFunc) (*gitexec.AgentSSH, error)) {
		sshHookMu.Lock()
		sshHook = fn
		sshHookMu.Unlock()
	}
	models := newSessionModels()
	p := &programHost{
		h: wiring.BuildForTest(t, wiring.WithLLMClient(models), wiring.WithGitStartSSH(func(ctx context.Context, dial sshproxy.DialFunc) (*gitexec.AgentSSH, error) {
			sshHookMu.RLock()
			fn := sshHook
			sshHookMu.RUnlock()
			if fn != nil {
				return fn(ctx, dial)
			}
			return gitexec.StartAgentSSH(ctx, dial)
		})),
		models: models,
	}
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")

	agentSock, userPub := startSigningSSHAgent(t)
	t.Setenv("SSH_AUTH_SOCK", agentSock)

	bareDir, _, seedOID := createBareSeededRepo(t)
	sshFixture := startSSHGitServer(t, bareDir, userPub)
	goodURL := fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", sshFixture.port)
	goodDest := fmt.Sprintf("127.0.0.1:%d", sshFixture.port)

	// Closed loopback port for dial_failed (connection_refused) and bad remote.
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "reserve closed port", err)
	closedPort := uint16(closedLn.Addr().(*net.TCPAddr).Port)
	_ = closedLn.Close()
	closedURL := fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", closedPort)
	closedDest := fmt.Sprintf("127.0.0.1:%d", closedPort)

	recorder := invocation.NewSQLRecorder(p.h.DB)

	// 1. Host setup failure -> GIT_SSH_TRANSPORT_SETUP_FAILED.
	setSSHHook(func(context.Context, sshproxy.DialFunc) (*gitexec.AgentSSH, error) {
		return nil, errors.New("listener bind failed")
	})
	setupDir := newSeededGitWorkDir(t)
	gittest.Run(t, setupDir, "remote", "add", "origin", goodURL)
	setupSess := p.withClientAt(t, setupDir, programModel(map[string]any{"command": "git ls-remote origin"}), wire.CreateSessionRequest{}, "")
	setupSess.h.SeedProgress(t, t.Context(), setupSess.session)
	testutil.FailErr(t, "setup failure prompt", setupSess.prompt(t.Context()))
	setSSHHook(nil)

	setupRaw := rawToolResult(t, p.h, setupSess.session, "program-1")
	if !strings.Contains(setupRaw, "GIT_SSH_TRANSPORT_SETUP_FAILED") || strings.Contains(setupRaw, "SSH_AUTH_SOCK") {
		t.Fatalf("expected GIT_SSH_TRANSPORT_SETUP_FAILED without agent speculation, got:\n%s", setupRaw)
	}
	setupOutcome := decodeProgramOutcomeFromToolResult(t, setupRaw)
	setupExec := singleGitExecution(t, recorder, setupSess.session, setupOutcome.ProgramID)
	if setupExec.Transport == nil || setupExec.Transport.Cause != "setup_failed" || setupExec.Transport.Stage != "host_setup" || setupExec.Transport.PolicyReason != "service_start_failed" {
		t.Fatalf("setup failure transport = %+v", setupExec.Transport)
	}

	// 2. Upstream dial failure (loopback port granted, port closed -> connection_refused).
	dialDir := newSeededGitWorkDir(t)
	gittest.Run(t, dialDir, "remote", "add", "origin", closedURL)
	dialSess := p.withClientAt(t, dialDir, programModel(map[string]any{"command": "git ls-remote origin"}), wire.CreateSessionRequest{}, "")
	dialSess.h.SeedProgress(t, t.Context(), dialSess.session)
	grantSSHAuthority(t, dialSess, agentSock, closedPort)
	testutil.FailErr(t, "dial failure prompt", dialSess.prompt(t.Context()))

	dialRaw := rawToolResult(t, p.h, dialSess.session, "program-1")
	if !strings.Contains(dialRaw, "GIT_SSH_TRANSPORT_DIAL_FAILED") || !strings.Contains(dialRaw, "connection_refused") || strings.Contains(dialRaw, "SSH_AUTH_SOCK") {
		t.Fatalf("expected GIT_SSH_TRANSPORT_DIAL_FAILED with connection_refused, got:\n%s", dialRaw)
	}
	dialOutcome := decodeProgramOutcomeFromToolResult(t, dialRaw)
	dialExec := singleGitExecution(t, recorder, dialSess.session, dialOutcome.ProgramID)
	if dialExec.Transport == nil || dialExec.Transport.Cause != "dial_failed" || dialExec.Transport.ErrorClass != "connection_refused" || dialExec.Transport.Destination != closedDest || len(dialExec.Transport.Attempts) != 1 || dialExec.Transport.Attempts[0].ReplyCode != 3 {
		t.Fatalf("dial failure transport = %+v", dialExec.Transport)
	}

	// 3. Absent host evidence -> explicit unknown outcome without unsupported causal claims.
	setSSHHook(func(ctx context.Context, dial sshproxy.DialFunc) (*gitexec.AgentSSH, error) {
		svc, err := gitexec.StartAgentSSH(ctx, dial)
		if err != nil {
			return nil, err
		}
		_ = svc.Close()
		return svc, nil
	})
	unknownDir := newSeededGitWorkDir(t)
	gittest.Run(t, unknownDir, "remote", "add", "origin", goodURL)
	unknownSess := p.withClientAt(t, unknownDir, programModel(map[string]any{"command": "git ls-remote origin"}), wire.CreateSessionRequest{}, "")
	unknownSess.h.SeedProgress(t, t.Context(), unknownSess.session)
	testutil.FailErr(t, "unknown transport prompt", unknownSess.prompt(t.Context()))
	setSSHHook(nil)

	unknownRaw := rawToolResult(t, p.h, unknownSess.session, "program-1")
	for _, forbidden := range []string{"GIT_SSH_TRANSPORT_DENIED", "GIT_SSH_TRANSPORT_DIAL_FAILED", "GIT_SSH_TRANSPORT_SETUP_FAILED", "SSH_AUTH_SOCK"} {
		if strings.Contains(unknownRaw, forbidden) {
			t.Fatalf("unknown transport outcome fabricated %s in:\n%s", forbidden, unknownRaw)
		}
	}
	unknownOutcome := decodeProgramOutcomeFromToolResult(t, unknownRaw)
	unknownExec := singleGitExecution(t, recorder, unknownSess.session, unknownOutcome.ProgramID)
	if unknownExec.Transport == nil || unknownExec.Transport.Cause != "unknown" || len(unknownExec.Transport.Attempts) != 0 {
		t.Fatalf("unknown transport = %+v", unknownExec.Transport)
	}

	// 4. Two concurrent calls to the same endpoint retain independent outcomes + unattributed background probe isolation.
	allowDir := newSeededGitWorkDir(t)
	gittest.Run(t, allowDir, "remote", "add", "origin", goodURL)
	allowSess := p.withClientAt(t, allowDir, programModel(map[string]any{
		"command":            "git fetch origin",
		"capability_request": map[string]any{"socket_paths": []any{agentSock}},
	}), wire.CreateSessionRequest{}, "")
	allowSess.h.SeedProgress(t, t.Context(), allowSess.session)
	grantSSHAuthority(t, allowSess, agentSock, sshFixture.port)

	denyDir := newSeededGitWorkDir(t)
	gittest.Run(t, denyDir, "remote", "add", "origin", goodURL)
	denySess := p.withClientAt(t, denyDir, programModel(map[string]any{"command": "git ls-remote origin"}), wire.CreateSessionRequest{}, "")
	denySess.h.SeedProgress(t, t.Context(), denySess.session)

	// Synchronize both sessions and fire unattributed host broker probes during the Git calls.
	probeConf := &confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkProxyOnly}
	probeLease, err := confine.BindAction(probeConf, confine.EgressCommand{
		SessionID: "s-probe", ActionID: "a-probe", ToolName: "probe", Image: "probe",
	})
	testutil.FailErr(t, "bind probe lease", err)
	probeLease.Close(context.Background())
	if probeConf.ProxyAddr == "" {
		t.Fatal("expected active host egress proxy address")
	}
	httpProxy := "http://" + probeConf.ProxyAddr
	var wg sync.WaitGroup
	wg.Add(3)
	var allowErr, denyErr error
	go func() {
		defer wg.Done()
		allowErr = allowSess.prompt(t.Context())
	}()
	go func() {
		defer wg.Done()
		denyErr = denySess.prompt(t.Context())
	}()
	go func() {
		defer wg.Done()
		for range 4 {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://unattributed.invalid/probe", nil)
			client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, httpProxy))}, Timeout: 2 * time.Second}
			if resp, err := client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
	}()
	wg.Wait()
	testutil.FailErr(t, "concurrent allow prompt", allowErr)
	testutil.FailErr(t, "concurrent deny prompt", denyErr)

	allowRaw := rawToolResult(t, p.h, allowSess.session, "program-1")
	for _, forbidden := range []string{"SANDBOX_BOUNDARY_REFUSED", "GIT_SSH_TRANSPORT_"} {
		if strings.Contains(allowRaw, forbidden) {
			t.Fatalf("background probe or sibling refusal tainted successful Git call with %s:\n%s", forbidden, allowRaw)
		}
	}
	allowOutcome := decodeProgramOutcomeFromToolResult(t, allowRaw)
	if allowOutcome.Status != 0 {
		t.Fatalf("allow session status = %d, raw = %s", allowOutcome.Status, allowRaw)
	}
	if gotOID := strings.TrimSpace(gittest.Run(t, allowDir, "rev-parse", "refs/remotes/origin/main")); gotOID != seedOID {
		t.Fatalf("allow session fetched %q, want %q", gotOID, seedOID)
	}
	allowExec := singleGitExecution(t, recorder, allowSess.session, allowOutcome.ProgramID)
	if allowExec.Transport == nil || allowExec.Transport.Cause != "established" || allowExec.Transport.Destination != goodDest {
		t.Fatalf("allow execution transport = %+v", allowExec.Transport)
	}

	denyRaw := rawToolResult(t, p.h, denySess.session, "program-1")
	assertRenderedTransportDenied(t, "concurrent-deny", denyRaw, goodDest)
	denyOutcome := decodeProgramOutcomeFromToolResult(t, denyRaw)
	denyExec := singleGitExecution(t, recorder, denySess.session, denyOutcome.ProgramID)
	if denyExec.Transport == nil || denyExec.Transport.Cause != "endpoint_denied" || denyExec.Transport.Destination != goodDest {
		t.Fatalf("deny execution transport = %+v", denyExec.Transport)
	}

	// 5. Multi-remote `git fetch --all` partial failure preserves already-fetched refs and attempt order.
	multiDir := newSeededGitWorkDir(t)
	gittest.Run(t, multiDir, "config", "fetch.parallel", "1")
	gittest.Run(t, multiDir, "remote", "add", "good", goodURL)
	gittest.Run(t, multiDir, "remote", "add", "bad", closedURL)

	multiSess := p.withClientAt(t, multiDir, programModel(map[string]any{
		"command":            "git fetch --all",
		"capability_request": map[string]any{"socket_paths": []any{agentSock}},
	}), wire.CreateSessionRequest{}, "")
	multiSess.h.SeedProgress(t, t.Context(), multiSess.session)
	// Grant only `good`'s loopback port so `good` succeeds and `bad` is refused by policy.
	grantSSHAuthority(t, multiSess, agentSock, sshFixture.port)
	testutil.FailErr(t, "multi-remote fetch prompt", multiSess.prompt(t.Context()))

	if gotOID := strings.TrimSpace(gittest.Run(t, multiDir, "rev-parse", "refs/remotes/good/main")); gotOID != seedOID {
		t.Fatalf("already-fetched good remote ref lost: got %q, want %q", gotOID, seedOID)
	}
	if refOut, err := execGitAllowFail(multiDir, "rev-parse", "--verify", "refs/remotes/bad/main"); err == nil {
		t.Fatalf("denied bad remote unexpectedly updated ref: %s", refOut)
	}

	multiRaw := rawToolResult(t, p.h, multiSess.session, "program-1")
	assertRenderedTransportDenied(t, "multi-remote", multiRaw, closedDest)
	multiOutcome := decodeProgramOutcomeFromToolResult(t, multiRaw)
	if multiOutcome.Status == 0 || len(multiOutcome.NonzeroExecutions) != 1 || multiOutcome.NonzeroExecutions[0].EffectCertainty != "unknown" {
		t.Fatalf("multi-remote partial failure outcome = %+v", multiOutcome)
	}
	multiExec := singleGitExecution(t, recorder, multiSess.session, multiOutcome.ProgramID)
	if multiExec.Transport == nil || multiExec.Transport.Cause != "endpoint_denied" || multiExec.Transport.Destination != closedDest || len(multiExec.Transport.Attempts) != 2 {
		t.Fatalf("multi-remote transport = %+v", multiExec.Transport)
	}
	first, second := multiExec.Transport.Attempts[0], multiExec.Transport.Attempts[1]
	if first.Ordinal != 1 || first.Destination != goodDest || first.Outcome != "established" || first.ReplyCode != 0 {
		t.Fatalf("first attempt = %+v", first)
	}
	if second.Ordinal != 2 || second.Destination != closedDest || second.Outcome != "endpoint_denied" || second.ReplyCode != 2 || second.PolicyReason != "loopback_port_ungranted" {
		t.Fatalf("second attempt = %+v", second)
	}
}

func rawToolResult(t *testing.T, h *wiring.Harness, sessionID, callID string) string {
	t.Helper()
	msgs, err := h.Store.Transcript.GetMessages(t.Context(), sessionID)
	testutil.FailErr(t, "read session messages", err)
	for _, raw := range msgs {
		msg := messageview.RedactMessage(raw)
		if msg.Role == wire.MessageRoleTool && msg.ToolResult != nil && msg.ToolResult.ToolCallID == callID {
			return raw.ToolResult.Content
		}
	}
	t.Fatalf("missing tool result for %s in %d messages", callID, len(msgs))
	return ""
}

func decodeProgramOutcomeFromToolResult(t *testing.T, raw string) command.ProgramOutcome {
	t.Helper()
	_, body, _, ok := hostmarker.SplitToolJSONBody(raw)
	if !ok {
		body = raw
	}
	var out command.ProgramOutcome
	testutil.FailErr(t, "decode ProgramOutcome", json.Unmarshal([]byte(body), &out))
	return out
}

func decodeProgramOutcomeFromWake(t *testing.T, content string) command.ProgramOutcome {
	t.Helper()
	_, body, _, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		_, body, ok = strings.Cut(content, "\n\n")
		if !ok {
			body = content
		}
	}
	// Strip any trailing post-tool guidance section after the JSON object.
	dec := json.NewDecoder(strings.NewReader(body))
	var out command.ProgramOutcome
	testutil.FailErr(t, "decode wake ProgramOutcome", dec.Decode(&out))
	return out
}

func decodeWaitReport(t *testing.T, raw string) string {
	t.Helper()
	_, body, _, ok := hostmarker.SplitToolJSONBody(raw)
	if !ok {
		body = raw
	}
	var res loopwake.WaitToolResult
	testutil.FailErr(t, "decode WaitToolResult", json.Unmarshal([]byte(body), &res))
	if res.Status != "process_done" || len(res.Conditions) != 1 || res.Conditions[0].Outcome != "satisfied" {
		t.Fatalf("unexpected WaitToolResult: %+v", res)
	}
	cond := res.Conditions[0]
	return cond.Report
}

func assertRenderedTransportDenied(t *testing.T, label, content, wantDest string) {
	t.Helper()
	for _, want := range []string{"GIT_SSH_TRANSPORT_DENIED", wantDest, "loopback_port_ungranted"} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s missing %q in:\n%s", label, want, content)
		}
	}
	if strings.Contains(content, "SSH_AUTH_SOCK") {
		t.Fatalf("%s speculated about SSH_AUTH_SOCK on pre-auth transport refusal:\n%s", label, content)
	}
}

func singleGitExecution(t *testing.T, recorder *invocation.SQLRecorder, sessionID, programID string) wire.CommandExecutionReceipt {
	t.Helper()
	page, err := recorder.ReadProgramPage(t.Context(), sessionID, programID, "", 10)
	testutil.FailErr(t, "ReadProgramPage "+programID, err)
	for _, row := range page.Executions {
		if row.NativeCommand == "git" || strings.HasPrefix(row.Command, "git ") {
			return row
		}
	}
	t.Fatalf("no git execution found in %+v", page.Executions)
	return wire.CommandExecutionReceipt{}
}

func execGitAllowFail(dir string, args ...string) (string, error) {
	bin, err := gitengine.BinaryPath()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func newSeededGitWorkDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "write seed file", os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644))
	gittest.InitCommit(t, dir, "seed")
	return dir
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	testutil.FailErr(t, "parse URL "+raw, err)
	return u
}
