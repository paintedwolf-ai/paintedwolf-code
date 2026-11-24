package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type command71CallOutput struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Status int    `json:"status"`
	State  string `json:"state"`
	Handle string `json:"handle"`
}

func (h *command71Harness) command71Source() string {
	return fmt.Sprintf(
		`cd %q && head -c 2200 "$(native_path -- @output/%s.stdout)"; grep -rn --include=*.go -F "sourceapi.Watch{" internal cmd | head -5; grep -rn --include=*.go -F "&Watch{" internal/api | head`,
		h.siblingLycaon, command71ArtifactHandle,
	)
}

func (h *command71Harness) invokeCommand(t *testing.T, ctx context.Context, callID, cmd string) (command71CallOutput, string) {
	t.Helper()
	out, id, err := h.tryInvokeCommand(ctx, callID, cmd)
	testutil.FailErr(t, "invoke command "+callID, err)
	return out, id
}

func (h *command71Harness) tryInvokeCommand(ctx context.Context, callID, cmd string) (command71CallOutput, string, error) {
	args := map[string]any{"command": cmd}
	contract, _ := toolcontract.Lookup("command")
	parent, err := h.recorder.Begin(ctx, invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: h.sessionID,
		ToolCallID: callID, ToolName: "command", Args: args, Contract: contract,
	})
	if err != nil {
		return command71CallOutput{}, "", err
	}
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{
			Agent: "implement", ProjectID: testdbseed.DefaultProjectID,
			SessionID: h.sessionID, RootSessionID: h.sessionID, ToolCallID: callID,
		},
		Invocation: tools.Invocation{ID: parent.ID},
		Source: tools.InvocationSource{
			Roots:               []projectroot.RootRef{{ID: "r1", Label: "primary", Path: h.primaryRoot, IsPrimary: true}},
			ActiveRootID:        "r1",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
		},
		Host: tools.InvocationHost{
			SessionScratchDir: h.scratchDir,
			SessionOutputDir:  h.outputDir,
		},
		Effects: tools.InvocationEffects{
			CredentialFiles: h.credFiles,
			GrepStops:       &tools.GrepStops{},
		},
	}
	raw, err := h.host.Executor.Invoke(ctx, "command", args, tc)
	if err != nil {
		return command71CallOutput{}, "", err
	}
	var out command71CallOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return command71CallOutput{}, "", err
	}
	return out, parent.ID, nil
}

func (h *command71Harness) grepProducerStatuses(t *testing.T, afterRowID int64) ([]int, int64) {
	t.Helper()
	rows, err := h.database.QueryContext(t.Context(), `
		SELECT rowid, COALESCE(status_code, -1) FROM invocation_program_executions
		WHERE operation_id = 'grep' AND rowid > ? ORDER BY rowid`, afterRowID)
	testutil.FailErr(t, "query grep producer executions", err)
	defer func() { _ = rows.Close() }()
	var statuses []int
	maxRow := afterRowID
	for rows.Next() {
		var rowID int64
		var status int
		testutil.FailErr(t, "scan grep execution", rows.Scan(&rowID, &status))
		statuses = append(statuses, status)
		if rowID > maxRow {
			maxRow = rowID
		}
	}
	return statuses, maxRow
}

func TestCommand71OracleColdWarmAndSeededControl(t *testing.T) {
	repochangetest.Acquire(t)
	h := newCommand71Harness(t)

	// Cold unmodified replay: first grep producer returns status 1 (no matches, no budget diagnostic),
	// second grep producer returns status 0 matching internal/api/sourceapi/handler.go, and head -c 2200
	// emits the exact first 2,200 bytes of the retained stdout artifact.
	coldOut, _ := h.invokeCommand(t, t.Context(), "cmd71-cold", h.command71Source())
	if coldOut.Handle != "" || coldOut.Status != 0 || coldOut.Stderr != "" {
		t.Fatalf("cold command #71 outcome = %+v", coldOut)
	}
	wantPrefix := h.seededStdout[:2200]
	if !strings.HasPrefix(coldOut.Stdout, wantPrefix) {
		t.Fatalf("cold stdout missing first 2,200 artifact bytes (len=%d)", len(coldOut.Stdout))
	}
	tail := coldOut.Stdout[2200:]
	if !strings.Contains(tail, "internal/api/sourceapi/handler.go:") || !strings.Contains(tail, "&Watch{") {
		t.Fatalf("cold stdout tail = %q, want handler.go &Watch{", tail)
	}
	coldStatuses, maxOrd := h.grepProducerStatuses(t, 0)
	if len(coldStatuses) != 2 || coldStatuses[0] != 1 || coldStatuses[1] != 0 {
		t.Fatalf("cold grep producer statuses = %v, want [1 0]", coldStatuses)
	}

	// Warm unmodified replay reuses directory membership and prunes learned negatives.
	warmOut, _ := h.invokeCommand(t, t.Context(), "cmd71-warm", h.command71Source())
	if warmOut.Stdout != coldOut.Stdout || warmOut.Stderr != "" || warmOut.Status != 0 {
		t.Fatalf("warm mismatch: status=%d stderr=%q", warmOut.Status, warmOut.Stderr)
	}
	warmStatuses, maxOrd := h.grepProducerStatuses(t, maxOrd)
	if len(warmStatuses) != 2 || warmStatuses[0] != 1 || warmStatuses[1] != 0 {
		t.Fatalf("warm grep producer statuses = %v, want [1 0]", warmStatuses)
	}

	h.logs.mu.Lock()
	discLogs := append([]map[string]int64(nil), h.logs.discovery...)
	contentLogs := append([]map[string]int64(nil), h.logs.content...)
	h.logs.mu.Unlock()
	if len(contentLogs) < 4 || len(discLogs) < 4 {
		t.Fatalf("expected cold+warm search logs, discovery=%d content=%d", len(discLogs), len(contentLogs))
	}
	var sawLearned, sawPruned, sawDirReuse bool
	for _, c := range contentLogs {
		sawLearned = sawLearned || c["learned_files"] > 0
		sawPruned = sawPruned || c["pruned_files"] > 0
	}
	for _, d := range discLogs {
		sawDirReuse = sawDirReuse || d["directory_reuses"] > 0
	}
	if !sawLearned || !sawPruned || !sawDirReuse {
		t.Fatalf("missing worktree catalog reuse metrics: learned=%v pruned=%v dirReuse=%v", sawLearned, sawPruned, sawDirReuse)
	}

	// Seeded positive control in registered sibling: both grep producers return status 0, and
	// sibling mutation causes zero primary-root credential builds.
	var screenPhases []secretmatch.ScreenPhaseRecord
	var spMu sync.Mutex
	unsub := secretmatch.RegisterPhaseObserver(func(rec secretmatch.ScreenPhaseRecord) {
		spMu.Lock()
		screenPhases = append(screenPhases, rec)
		spMu.Unlock()
	})
	defer unsub()

	seedFile := filepath.Join(h.siblingLycaon, "cmd/lycaon/watch_seed.go")
	testutil.FailErr(t, "write sibling watch seed", os.WriteFile(seedFile, []byte("package main\nvar _ = sourceapi.Watch{}\n"), 0o644))
	sibEp := repochange.Advance(filepath.Dir(h.siblingLycaon))
	repochange.Notify(t.Context(), repochange.Event{
		ProjectDir: filepath.Dir(h.siblingLycaon), Kind: repochange.WorktreeChanged,
		Paths: []string{"lycaon/cmd/lycaon/watch_seed.go"}, EpochBootID: sibEp.BootID, WorktreeEpoch: sibEp.Value,
	})

	seededOut, _ := h.invokeCommand(t, t.Context(), "cmd71-seeded", h.command71Source())
	if seededOut.Status != 0 || !strings.Contains(seededOut.Stdout, "cmd/lycaon/watch_seed.go:2:") || !strings.Contains(seededOut.Stdout, "internal/api/sourceapi/handler.go:") {
		t.Fatalf("seeded positive control output = %+v", seededOut)
	}
	seededStatuses, _ := h.grepProducerStatuses(t, maxOrd)
	if len(seededStatuses) != 2 || seededStatuses[0] != 0 || seededStatuses[1] != 0 {
		t.Fatalf("seeded grep producer statuses = %v, want [0 0]", seededStatuses)
	}
	if got := filterScreenPhases(&spMu, &screenPhases, "credential_build"); len(got) != 0 {
		t.Fatalf("sibling edit triggered attached primary credential builds: %+v", got)
	}
	var execRows int
	testutil.FailErr(t, "count recorded program executions", h.database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM invocation_program_executions`).Scan(&execRows))
	if execRows < 9 {
		t.Fatalf("expected recorded program execution receipts for head and grep stages, got %d", execRows)
	}
}

func TestCommand71BackgroundToForegroundDispatchBarriersAndDeniedControl(t *testing.T) {
	for _, ordering := range []string{"recorded_order", "publication_races_evidence"} {
		t.Run(ordering, func(t *testing.T) {
			repochangetest.Acquire(t)
			h := newCommand71Harness(t)

			var awaitMu, screenMu sync.Mutex
			var awaitPhases []awaitstore.PhaseRecord
			var screenPhases []secretmatch.ScreenPhaseRecord
			unsubAwait := awaitstore.RegisterPhaseObserver(func(rec awaitstore.PhaseRecord) {
				awaitMu.Lock()
				awaitPhases = append(awaitPhases, rec)
				awaitMu.Unlock()
			})
			defer unsubAwait()
			unsubScreen := secretmatch.RegisterPhaseObserver(func(rec secretmatch.ScreenPhaseRecord) {
				screenMu.Lock()
				screenPhases = append(screenPhases, rec)
				screenMu.Unlock()
			})
			defer unsubScreen()

			// Warm initial credential index so admission starts clean, then hold unrelated project busy.
			testutil.FailErr(t, "warm primary credentials", h.credFiles.Await(t.Context(), tools.CredentialWait{ProjectID: testdbseed.DefaultProjectID}))
			repochange.WaitQuiet(t.Context(), h.primaryRoot, repochange.DefaultDebounce, 2*time.Second)
			testutil.FailErr(t, "drain startup events", h.credFiles.Await(t.Context(), tools.CredentialWait{ProjectID: testdbseed.DefaultProjectID}))
			clearScreenPhases(&screenMu, &screenPhases)

			unrelEntered := make(chan struct{})
			unrelRelease := make(chan struct{})
			var unrelOnce, closeUnrel sync.Once
			releaseUnrel := func() { closeUnrel.Do(func() { close(unrelRelease) }) }
			t.Cleanup(releaseUnrel)
			credEntered := make(chan struct{})
			credRelease := make(chan struct{})
			var credOnce, closeCred sync.Once
			releaseCred := func() { closeCred.Do(func() { close(credRelease) }) }
			t.Cleanup(releaseCred)
			h.credFiles.index.onDir = func(rootPath, _ string) {
				if rootPath != h.primaryRoot {
					unrelOnce.Do(func() {
						close(unrelEntered)
						<-unrelRelease
					})
				}
			}
			h.credFiles.index.onScan = func(rootPath, _ string) {
				if rootPath == h.primaryRoot {
					credOnce.Do(func() {
						close(credEntered)
						<-credRelease
					})
				}
			}

			// Start unrelated observer and keep it held throughout foreground dispatch.
			unrelDone := make(chan error, 1)
			go func() {
				unrelDone <- h.credFiles.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-unrelated"})
			}()
			<-unrelEntered

			// Hold Command #71 before first native execution ("head") and simulate unattributed pre-cd interval.
			nativeEntered := make(chan struct{})
			nativeRelease := make(chan struct{})
			var nativeOnce, closeNative sync.Once
			releaseNative := func() { closeNative.Do(func() { close(nativeRelease) }) }
			t.Cleanup(releaseNative)
			h.nativeMu.Lock()
			h.onFirstNative = func() {
				nativeOnce.Do(func() {
					close(nativeEntered)
					<-nativeRelease
				})
			}
			h.nativeMu.Unlock()

			dispatchedAt := time.Now().UTC().Add(-4 * time.Minute)
			type cmdResult struct {
				out       command71CallOutput
				receiptID string
				err       error
			}
			cmdCh := make(chan cmdResult, 1)
			go func() {
				out, recID, invErr := h.tryInvokeCommand(awaitstore.WithDispatchTime(t.Context(), dispatchedAt), "call-71", h.command71Source())
				cmdCh <- cmdResult{out: out, receiptID: recID, err: invErr}
			}()

			select {
			case <-nativeEntered:
			case early := <-cmdCh:
				t.Fatalf("command #71 finished before native barrier: err=%v out=%+v", early.err, early.out)
			}
			// Advance ManualClock past 30s budget so Command #71 promotes to background.
			testutil.WaitFor(t, "foreground budget timer armed", func() bool { return h.programClock.Pending() > 0 })
			h.programClock.Advance(31 * time.Second)
			promoted := <-cmdCh
			testutil.FailErr(t, "promoted command #71", promoted.err)
			if promoted.out.Handle == "" {
				t.Fatalf("expected promoted background handle, got %+v", promoted.out)
			}

			// Park a terminal wait on the promoted handle while Command #71 is still completing.
			waitRes, err := h.waitReg.Run(t.Context(), "wait", map[string]any{
				"until_complete": true,
				"conditions":     []any{map[string]any{"kind": "process_done", "handles": []any{promoted.out.Handle}}},
			}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: h.sessionID, ProjectID: testdbseed.DefaultProjectID, ToolCallID: "wait-cmd71"}})
			testutil.FailErr(t, "park wait on command #71", err)
			if !strings.Contains(waitRes, `"status":"parked"`) {
				t.Fatalf("expected parked lease, got %s", waitRes)
			}

			// Introduce a real credential rotation in primaryRoot/.env and start next foreground request.
			const rotatedCred = "cmd71-rotated-secret-77665544"
			const promptText = "Summarize watch findings; env=" + rotatedCred
			testutil.FailErr(t, "rotate primary .env", os.WriteFile(filepath.Join(h.primaryRoot, ".env"), []byte("API_TOKEN="+rotatedCred+"\n"), 0o600))
			rotEp := repochange.Advance(h.primaryRoot)
			repochange.Notify(t.Context(), repochange.Event{
				ProjectDir: h.primaryRoot, Kind: repochange.WorktreeChanged, Paths: []string{".env"},
				EpochBootID: rotEp.BootID, WorktreeEpoch: rotEp.Value,
			})

			fgCtx := curationctx.WithSession(t.Context(), curationctx.Session{
				SessionID: h.sessionID, ProjectID: testdbseed.DefaultProjectID, ProjectDir: h.primaryRoot,
			})
			fgDone := make(chan error, 1)
			go func() {
				_, fgErr := h.fgClient.Complete(fgCtx, modelcall.CompletionRequest{
					Debug:    modelcall.RequestDebug{CallID: "fg-after-71", SessionID: h.sessionID, RootSessionID: h.sessionID},
					Messages: []api.Message{{Role: api.MessageRoleUser, Content: promptText}},
				})
				fgDone <- fgErr
			}()
			<-credEntered

			// No outbound call may occur while credential evidence is still reconciling.
			if h.provider.calls.Load() != 0 {
				t.Fatalf("provider dispatched with stale evidence: calls=%d", h.provider.calls.Load())
			}

			// Release barriers in the requested order while unrelated project remains held!
			if ordering == "recorded_order" {
				releaseNative()
				comp := <-h.completions
				h.procCtrl.HandleCommandCompletion(t.Context(), comp)
				releaseCred()
			} else {
				releaseCred()
				releaseNative()
				comp := <-h.completions
				h.procCtrl.HandleCommandCompletion(t.Context(), comp)
			}

			testutil.FailErr(t, "foreground dispatch after barriers", <-fgDone)
			if h.provider.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1 while unrelated work remains held", h.provider.calls.Load())
			}
			h.provider.mu.Lock()
			var sentBuilder strings.Builder
			for _, m := range h.provider.dispatched[0].Messages {
				sentBuilder.WriteString(m.Content)
			}
			h.provider.mu.Unlock()
			exposure.Absent(t, sentBuilder.String(), rotatedCred)

			// Verify 1 terminal delivery and 0 remaining active wait leases.
			testutil.WaitFor(t, "wait lease delivered", func() bool {
				active, aErr := h.waitStore.Active(t.Context())
				return aErr == nil && len(active) == 0
			})
			h.loop.Turns.WaitForAsyncTurns(t.Context())

			// Denied-screen control: 0 new provider dispatches or duplicate builds; command output preserved.
			buildsBeforeDenied := len(filterScreenPhases(&screenMu, &screenPhases, "credential_build"))
			h.askMu.Lock()
			h.askDecision = secretmatch.Withhold
			h.askMu.Unlock()
			_, withholdErr := h.fgClient.Complete(fgCtx, modelcall.CompletionRequest{
				Debug:    modelcall.RequestDebug{CallID: "fg-denied-71", SessionID: h.sessionID, RootSessionID: h.sessionID},
				Messages: []api.Message{{Role: api.MessageRoleUser, Content: promptText}},
			})
			if !errors.Is(withholdErr, llm.ErrModelRequestSecretWithheld) {
				t.Fatalf("denied screen err = %v, want withheld", withholdErr)
			}
			if got := len(filterScreenPhases(&screenMu, &screenPhases, "credential_build")); got != buildsBeforeDenied {
				t.Fatalf("covered generation triggered duplicate build during denied control: %d -> %d", buildsBeforeDenied, got)
			}
			if h.provider.calls.Load() != 1 {
				t.Fatalf("denied screen dispatched to provider: calls=%d", h.provider.calls.Load())
			}
			tailSnap, err := h.bg.Snapshot(t.Context(), h.sessionID, promoted.out.Handle, 64*1024)
			testutil.FailErr(t, "read preserved background output", err)
			if !strings.Contains(tailSnap.Tail, "internal/api/sourceapi/handler.go:") {
				t.Fatalf("background command terminal output lost handler.go match: %q", tailSnap.Tail)
			}

			// Release unrelated observer cleanly.
			h.credFiles.index.onDir = nil
			h.credFiles.index.onScan = nil
			releaseUnrel()
			testutil.FailErr(t, "unrelated observer finishes", <-unrelDone)

			// Verify correlated phase records and zero secret/prompt leakage.
			assertCommand71PhaseRecords(t, &awaitMu, awaitPhases, &screenMu, screenPhases, promoted.receiptID, rotatedCred, promptText)
		})
	}
}

func assertCommand71PhaseRecords(
	t *testing.T,
	awaitMu *sync.Mutex, awaitPhases []awaitstore.PhaseRecord,
	screenMu *sync.Mutex, screenPhases []secretmatch.ScreenPhaseRecord,
	receiptID, secretVal, promptText string,
) {
	t.Helper()
	awaitMu.Lock()
	ap := append([]awaitstore.PhaseRecord(nil), awaitPhases...)
	awaitMu.Unlock()
	screenMu.Lock()
	sp := append([]secretmatch.ScreenPhaseRecord(nil), screenPhases...)
	screenMu.Unlock()

	var admitEnter, admitExit, reportPub, txCommit int
	for _, rec := range ap {
		if rec.Phase == "tool_admission_enter" && rec.Handle == receiptID && rec.Outcome == "command" && rec.DurationMS >= 239000 {
			admitEnter++
		}
		if rec.Phase == "tool_admission_exit" && rec.Handle == receiptID && rec.Outcome == "command" {
			admitExit++
		}
		if rec.Phase == "process_report_published" {
			reportPub++
		}
		if rec.Phase == "transcript_result_commit" && rec.Tool == "wait" {
			txCommit++
		}
	}
	if admitEnter != 1 || admitExit != 1 || reportPub != 1 || txCommit != 1 {
		t.Fatalf("await phase counts: admitEnter=%d admitExit=%d reportPub=%d txCommit=%d", admitEnter, admitExit, reportPub, txCommit)
	}

	var primaryBuilds, fgMatcher, fgDecision int
	for _, rec := range sp {
		if rec.Phase == "credential_build" && rec.ProjectID == testdbseed.DefaultProjectID {
			primaryBuilds++
		}
		if rec.CallID == "fg-after-71" && (rec.Phase == "matcher_scan" || rec.Phase == "matcher_redaction") {
			fgMatcher++
		}
		if rec.CallID == "fg-after-71" && rec.Phase == "person_decision" {
			fgDecision++
		}
	}
	// Incremental rotated-env refresh with zero duplicate builds on the covered generation.
	if primaryBuilds < 1 || primaryBuilds > 2 || fgMatcher < 1 || fgDecision != 1 {
		t.Fatalf("screen phase counts: primaryBuilds=%d fgMatcher=%d fgDecision=%d", primaryBuilds, fgMatcher, fgDecision)
	}

	rawAwait, err := json.Marshal(ap)
	testutil.FailErr(t, "marshal await phases", err)
	rawScreen, err := json.Marshal(sp)
	testutil.FailErr(t, "marshal screen phases", err)
	combined := string(rawAwait) + string(rawScreen)
	if strings.Contains(combined, secretVal) || strings.Contains(combined, promptText) {
		t.Fatalf("diagnostic records leaked secret or prompt content")
	}
}
