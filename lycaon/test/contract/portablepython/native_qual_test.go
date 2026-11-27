package portablepython

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func newNativeQualService(t *testing.T, receiptDir string) *portablepython.Service {
	t.Helper()
	if os.Getenv("PW_REQUIRE_NATIVE_QUAL") == "1" && strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) != "" {
		t.Fatalf("%s must be UNSET during installed-product qualification", portablepython.EnvImageDir)
	}
	cfg := portablepython.ServiceConfig{
		ScratchRoot: t.TempDir(),
	}
	if receiptDir != "" {
		sink, err := portablepython.NewFileReceiptStore(receiptDir)
		contractcheck.FailErr(t, "create FileReceiptStore", err)
		cfg.ReceiptSink = sink
	}
	svc, err := portablepython.NewService(context.Background(), cfg)
	if err != nil {
		if os.Getenv("PW_REQUIRE_NATIVE_QUAL") == "1" {
			t.Fatalf("failed to initialize Portable Python service from installed image: %v", err)
		}
		t.Skipf("no installed Portable Python image discovered (set LYCAON_ENGINE_ROOT or stage image): %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

type qualEditorBridge struct {
	mu     sync.Mutex
	drafts map[string]pythondocuments.EditorDraft
}

func (e *qualEditorBridge) CaptureDraft(_ context.Context, _, _, relPath string) (pythondocuments.EditorDraft, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.drafts[relPath]
	if !ok {
		return pythondocuments.EditorDraft{}, false, nil
	}
	d.Bytes = append([]byte(nil), d.Bytes...)
	return d, true, nil
}

func (e *qualEditorBridge) PublishDraft(
	_ context.Context, rootID, branch, relPath string, baseRev pythondocuments.DocumentRevision, data []byte,
) (pythondocuments.DocumentRevision, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.drafts[relPath]
	if !ok {
		return pythondocuments.DocumentRevision{}, false, nil
	}
	d.Bytes = append([]byte(nil), data...)
	d.Revision = d.Revision + "+1"
	e.drafts[relPath] = d
	return pythondocuments.DocumentRevision{
		RootID: rootID, Branch: branch, DocumentID: d.DocumentID,
		DocumentRevision: d.Revision, DiskSHA256: d.BaseSHA256, UnsavedDraft: true,
	}, true, nil
}

func execQualScript(t *testing.T, svc *portablepython.Service, dir, script string, args []string) *portablepython.ToolResponse {
	t.Helper()
	roots := []pythondocuments.HostRootConfig{{ID: "ws-qual", Dir: dir}}
	raw := map[string]any{"script": script}
	if len(args) > 0 {
		raw["args"] = args
	}
	resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
		RawArgs:      raw,
		CallID:       "call-qual-" + script,
		SessionID:    "sess-native-qual",
		Actor:        "coordinator",
		View:         "primary",
		Surface:      "implement_investigate",
		Roots:        roots,
		MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		Limits:       pythonruntime.ResourceLimits{Timeout: 120 * time.Second},
	})
	contractcheck.FailErr(t, "execute "+script, err)
	if !resp.CleanCompletion || resp.ExitCode != 0 {
		t.Fatalf("%s failed: exit=%d clean=%v stop=%+v stderr=%q", script, resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.Stderr)
	}
	return resp
}

func TestNativeQual_SixFrozenWorkloads(t *testing.T) {
	receiptDir := t.TempDir()
	svc := newNativeQualService(t, receiptDir)
	files := loadFrozenFixtureZip(t)

	t.Run("1_dependency_review", func(t *testing.T) {
		dir := extractWorkflowDir(t, files, "dependency-review")
		execQualScript(t, svc, dir, "program.py", nil)
		gotCSV, err := os.ReadFile(filepath.Join(dir, "dependency-changes.csv"))
		contractcheck.FailErr(t, "read dependency-changes.csv", err)
		exp, err := ComputeDepDiff(
			files["dependency-review/before/uv.lock"], files["dependency-review/after/uv.lock"],
			files["dependency-review/before/train-host/requirements-cuda.txt"], files["dependency-review/after/train-host/requirements-cuda.txt"],
		)
		contractcheck.FailErr(t, "compute dep diff", err)
		contractcheck.FailErr(t, "grade dependency-changes.csv", GradeDependencyDiff(exp, gotCSV, map[string][]string{"before": nil, "after": nil}))
	})

	t.Run("2_licenses_csv", func(t *testing.T) {
		dir := extractWorkflowDir(t, files, "licenses")
		execQualScript(t, svc, dir, "program.py", nil)
		gotCSV, err := os.ReadFile(filepath.Join(dir, "holdout_repos.csv"))
		contractcheck.FailErr(t, "read holdout_repos.csv", err)
		contractcheck.FailErr(t, "grade holdout_repos.csv", GradeLicenseCSV(files["licenses/config/repos.yaml"], gotCSV))
	})

	t.Run("3_workbook_xlsx_saved_and_reopened", func(t *testing.T) {
		dir := extractWorkflowDir(t, files, "workbook")
		execQualScript(t, svc, dir, "program.py", nil)
		gotXLSX, err := os.ReadFile(filepath.Join(dir, "repositories.xlsx"))
		contractcheck.FailErr(t, "read repositories.xlsx", err)
		contractcheck.FailErr(t, "grade workbook frozen", GradeRepoWorkbookWithMode(files["workbook/config/repos.yaml"], gotXLSX, WorkbookFrozenReplay))
		contractcheck.FailErr(t, "grade workbook live", GradeRepoWorkbookWithMode(files["workbook/config/repos.yaml"], gotXLSX, WorkbookLiveDeliverable))
	})

	t.Run("4_release_index", func(t *testing.T) {
		dir := extractWorkflowDir(t, files, "release-index")
		execQualScript(t, svc, dir, "program.py", nil)
		gotJSON, err := os.ReadFile(filepath.Join(dir, "release-index.json"))
		contractcheck.FailErr(t, "read release-index.json", err)
		anchors := make(map[string][]byte)
		for k, v := range files {
			if strings.HasPrefix(k, "release-index/releases/") && strings.HasSuffix(k, ".json") {
				anchors[filepath.Base(k)] = v
			}
		}
		contractcheck.FailErr(t, "grade release-index.json", GradeReleaseIndex(anchors, gotJSON))
	})

	t.Run("5_markdown_links", func(t *testing.T) {
		dir := extractWorkflowDir(t, files, "markdown-links")
		execQualScript(t, svc, dir, "program.py", nil)
		gotCSV, err := os.ReadFile(filepath.Join(dir, "links.csv"))
		contractcheck.FailErr(t, "read links.csv", err)
		mdInputs := make(map[string]string)
		for k, v := range files {
			if strings.HasPrefix(k, "markdown-links/") && strings.HasSuffix(k, ".md") {
				mdInputs[strings.TrimPrefix(k, "markdown-links/")] = string(v)
			}
		}
		contractcheck.FailErr(t, "grade links.csv", GradeMarkdownLinks(mdInputs, gotCSV))
	})

	t.Run("6_reproducible_archive", func(t *testing.T) {
		dir := extractWorkflowDir(t, files, "archive")
		execQualScript(t, svc, dir, "build_handoff.py", []string{"handoff-a.zip"})
		execQualScript(t, svc, dir, "build_handoff.py", []string{"handoff-b.zip"})
		zipA, err := os.ReadFile(filepath.Join(dir, "handoff-a.zip"))
		contractcheck.FailErr(t, "read handoff-a.zip", err)
		zipB, err := os.ReadFile(filepath.Join(dir, "handoff-b.zip"))
		contractcheck.FailErr(t, "read handoff-b.zip", err)
		anchors := make(map[string][]byte)
		for k, v := range files {
			if strings.HasPrefix(k, "archive/releases/") && strings.HasSuffix(k, ".json") {
				anchors[filepath.Base(k)] = v
			}
		}
		contractcheck.FailErr(t, "grade reproducible archive", GradeReleaseArchive(anchors, zipA, zipB))
	})

	t.Run("7_editor_bridge_worker_and_resumed_receipts", func(t *testing.T) {
		wsDir := t.TempDir()
		contractcheck.FailErr(t, "write disk sentinel", os.WriteFile(filepath.Join(wsDir, "draft.txt"), []byte("DISK_STALE"), 0o644))
		editor := &qualEditorBridge{drafts: map[string]pythondocuments.EditorDraft{
			"draft.txt": {DocumentID: "doc-1", Revision: "rev-3", Bytes: []byte("UNSAVED_DRAFT_42"), BaseSHA256: pythondocuments.SHA256Hex([]byte("UNSAVED_DRAFT_42")), Dirty: true},
		}}
		roots := []pythondocuments.HostRootConfig{{ID: "ws-worker", Branch: "worker-qual-1", Dir: wsDir}}
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			RawArgs: map[string]any{"code": `print(open("draft.txt").read())`},
			CallID:  "call-worker-editor", SessionID: "sess-resumed", Actor: "worker-1", View: "worker-qual-1", Surface: "agent",
			Roots: roots, Editor: editor, MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Limits: pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
		})
		contractcheck.FailErr(t, "execute editor bridge read", err)
		if !resp.CleanCompletion || strings.TrimSpace(resp.Stdout) != "UNSAVED_DRAFT_42" {
			t.Fatalf("expected UNSAVED_DRAFT_42, got stdout=%q clean=%v", resp.Stdout, resp.CleanCompletion)
		}
		reopened, err := portablepython.NewFileReceiptStore(receiptDir)
		contractcheck.FailErr(t, "reopen FileReceiptStore", err)
		recs, err := reopened.LoadExecutions()
		contractcheck.FailErr(t, "load persisted executions", err)
		if len(recs) == 0 {
			t.Fatal("expected persisted execution receipt after reopening store")
		}
	})
}

func TestNativeQual_PairedDenialAndFuelControls(t *testing.T) {
	svc := newNativeQualService(t, "")
	ctx := context.Background()

	t.Run("denied_file_open_receipt_parity", func(t *testing.T) {
		wsDir := t.TempDir()
		sentinel := "TOP_SECRET_BLOCKED_SENTINEL_9911"
		contractcheck.FailErr(t, "write blocked.txt", os.WriteFile(filepath.Join(wsDir, "blocked.txt"), []byte(sentinel), 0o644))
		roots := []pythondocuments.HostRootConfig{{ID: "ws-deny", Dir: wsDir}}
		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{"code": `
try:
    print(open("blocked.txt").read())
except OSError as e:
    print("DENIED_ERRNO", e.errno)
`},
			CallID: "call-deny-1", SessionID: "sess-deny", Actor: "coordinator", View: "primary", Surface: "agent",
			Roots: roots, MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Policy: func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
				if op.Target.RelPath == "blocked.txt" {
					return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionDeny, Condition: pythonadmission.ConditionEffectDenied, Reason: "blocked by qualification policy"}
				}
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
			},
			Limits: pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
		})
		contractcheck.FailErr(t, "execute denied open", err)
		if strings.Contains(resp.Stdout, sentinel) || strings.Contains(resp.Stderr, sentinel) {
			t.Fatalf("denied file sentinel disclosed in output: stdout=%q stderr=%q", resp.Stdout, resp.Stderr)
		}
		if resp.Settlement != nil && len(resp.Settlement.Published) > 0 {
			t.Fatalf("unexpected publication on denied open: %+v", resp.Settlement.Published)
		}
		var matched *pythonadmission.AdmissionRecord
		if resp.AdmissionEvidence != nil {
			for _, rec := range resp.AdmissionEvidence.Admissions() {
				if rec.AuthoritativeTarget.RelPath == "blocked.txt" && rec.Decision == pythonruntime.DecisionDeny {
					r := rec
					matched = &r
					break
				}
			}
		}
		if matched == nil {
			t.Fatalf("missing denied AdmissionRecord for blocked.txt")
		}
		if matched.Actor != "coordinator" || matched.CallID != "call-deny-1" || matched.Reason != "blocked by qualification policy" || matched.Family == "" || matched.Operation == "" {
			t.Fatalf("admission record binding mismatch: %+v", matched)
		}
	})

	t.Run("deterministic_fuel_exact_boundary_and_one_unit_beyond", func(t *testing.T) {
		wsDir := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws-fuel", Dir: wsDir}}
		code := "s = 0\nfor i in range(400):\n    s += i\nprint(s)\n"
		baseReq := func(maxFuel uint64) portablepython.ToolRequest {
			return portablepython.ToolRequest{
				RawArgs: map[string]any{"code": code},
				CallID:  "call-fuel", Actor: "coordinator", View: "primary", Surface: "agent", Roots: roots,
				Limits: pythonruntime.ResourceLimits{MaxGuestWork: maxFuel, Timeout: 60 * time.Second},
			}
		}
		measure, err := svc.ExecuteTool(ctx, baseReq(portablepython.DefaultMaxGuestWork))
		contractcheck.FailErr(t, "measure exact fuel", err)
		if !measure.CleanCompletion || measure.Usage.GuestWork == 0 {
			t.Fatalf("baseline fuel measurement failed: clean=%v usage=%+v", measure.CleanCompletion, measure.Usage)
		}
		exact := measure.Usage.GuestWork

		atExact, err := svc.ExecuteTool(ctx, baseReq(exact))
		contractcheck.FailErr(t, "execute at exact fuel boundary", err)
		if !atExact.CleanCompletion || atExact.ExitCode != 0 || atExact.Stop != nil {
			t.Fatalf("expected pass at exact fuel=%d, got clean=%v exit=%d stop=%+v", exact, atExact.CleanCompletion, atExact.ExitCode, atExact.Stop)
		}

		oneBelow, err := svc.ExecuteTool(ctx, baseReq(exact-1))
		contractcheck.FailErr(t, "execute one unit below fuel boundary", err)
		if oneBelow.CleanCompletion || oneBelow.Stop == nil || oneBelow.Stop.Code != pythonruntime.StopWorkLimitExceeded {
			t.Fatalf("expected StopWorkLimitExceeded at fuel=%d, got clean=%v stop=%+v", exact-1, oneBelow.CleanCompletion, oneBelow.Stop)
		}
	})
}

func TestNativeQual_LifecycleAndIsolationMatrix(t *testing.T) {
	svc := newNativeQualService(t, "")

	t.Run("async_cancel_cpu_loop_with_ample_fuel", func(t *testing.T) {
		wsDir := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{"code": "while True:\n    pass\n"},
			CallID:  "call-cancel-cpu", Actor: "coordinator", View: "primary", Surface: "agent",
			Roots:  []pythondocuments.HostRootConfig{{ID: "ws-cpu", Dir: wsDir}},
			Limits: pythonruntime.ResourceLimits{MaxGuestWork: 10_000_000_000, Timeout: 30 * time.Second},
		})
		contractcheck.FailErr(t, "cancel pure CPU loop", err)
		if resp.CleanCompletion || resp.Stop == nil || resp.Stop.Code != pythonruntime.StopCancelled {
			t.Fatalf("expected StopCancelled, got clean=%v stop=%+v", resp.CleanCompletion, resp.Stop)
		}
	})

	t.Run("hold_and_resume_exactly_once_and_interrupt_pending", func(t *testing.T) {
		wsDir := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws-hold", Dir: wsDir}}
		broker := pythonadmission.NewApprovalBroker(pythonadmission.ApprovalBrokerConfig{})
		holdPolicy := func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
			if op.Target.RelPath == "held.txt" {
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionHold, Reason: "require approval"}
			}
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
		}
		go func() {
			for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				for _, l := range broker.Registry().List() {
					if l.State == pythonadmission.LeasePending {
						_ = broker.Approve(l.LeaseID, "reviewer")
						return
					}
				}
			}
		}()
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			RawArgs: map[string]any{"code": `open("held.txt", "w").write("APPROVED_ONCE")`},
			CallID:  "call-hold-ok", Actor: "coordinator", View: "primary", Surface: "agent",
			Roots: roots, Broker: broker, Policy: holdPolicy,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Limits:       pythonruntime.ResourceLimits{Timeout: 30 * time.Second},
		})
		contractcheck.FailErr(t, "execute held write", err)
		got, err := os.ReadFile(filepath.Join(wsDir, "held.txt"))
		if !resp.CleanCompletion || err != nil || string(got) != "APPROVED_ONCE" {
			t.Fatalf("held resume failed: clean=%v err=%v got=%q", resp.CleanCompletion, err, string(got))
		}

		wsInt := t.TempDir()
		rootsInt := []pythondocuments.HostRootConfig{{ID: "ws-int", Dir: wsInt}}
		brokerInt := pythonadmission.NewApprovalBroker(pythonadmission.ApprovalBrokerConfig{})
		ctxInt, cancelInt := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancelInt()
		respInt, err := svc.ExecuteTool(ctxInt, portablepython.ToolRequest{
			RawArgs: map[string]any{"code": `open("held.txt", "w").write("SHOULD_NOT_PUBLISH")`},
			CallID:  "call-hold-int", Actor: "coordinator", View: "primary", Surface: "agent",
			Roots: rootsInt, Broker: brokerInt, Policy: holdPolicy,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, rootsInt),
			Limits:       pythonruntime.ResourceLimits{Timeout: 30 * time.Second},
		})
		contractcheck.FailErr(t, "interrupt pending approval", err)
		if respInt.CleanCompletion {
			t.Fatal("expected CleanCompletion=false on interrupted approval")
		}
		if _, statErr := os.Stat(filepath.Join(wsInt, "held.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("interrupted approval must not publish held.txt, statErr=%v", statErr)
		}
	})

	t.Run("exception_rollback_and_resource_limits", func(t *testing.T) {
		wsDir := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws-rb", Dir: wsDir}}
		door := native.NewPythonMutationDoor(tools.ToolContext{}, roots)
		rb, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			RawArgs: map[string]any{"code": "open('partial.txt','w').write('dirty')\nraise RuntimeError('boom')\n"},
			CallID:  "call-rb", Actor: "coordinator", View: "primary", Surface: "agent",
			Roots: roots, MutationDoor: door, Limits: pythonruntime.ResourceLimits{Timeout: 30 * time.Second},
		})
		contractcheck.FailErr(t, "execute exception rollback", err)
		if rb.CleanCompletion {
			t.Fatal("expected CleanCompletion=false on uncaught exception")
		}
		if _, statErr := os.Stat(filepath.Join(wsDir, "partial.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("partial.txt must be rolled back on exception, statErr=%v", statErr)
		}

		outResp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			RawArgs: map[string]any{"code": "print('X' * 131072)"},
			CallID:  "call-outlim", Actor: "coordinator", View: "primary", Surface: "agent",
			Roots: roots, Limits: pythonruntime.ResourceLimits{MaxOutputBytes: 4096, Timeout: 30 * time.Second},
		})
		contractcheck.FailErr(t, "execute output limit", err)
		if outResp.CleanCompletion && !outResp.StdoutTruncated {
			t.Fatalf("expected output bound enforcement, got clean=%v truncated=%v stop=%+v", outResp.CleanCompletion, outResp.StdoutTruncated, outResp.Stop)
		}

		memResp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			RawArgs: map[string]any{"code": "a = bytearray(32 * 1024 * 1024)\nprint(len(a))\n"},
			CallID:  "call-memlim", Actor: "coordinator", View: "primary", Surface: "agent",
			Roots: roots, Limits: pythonruntime.ResourceLimits{MaxMemoryPages: 256, Timeout: 30 * time.Second},
		})
		contractcheck.FailErr(t, "execute memory limit", err)
		if memResp.CleanCompletion && memResp.ExitCode == 0 {
			t.Fatalf("expected memory limit failure on 32 MiB alloc under 16 MiB cap")
		}
	})

	t.Run("interleaved_concurrent_guests_isolation_and_recovery", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan string, 4)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				dir := t.TempDir()
				roots := []pythondocuments.HostRootConfig{{ID: "ws-conc", Dir: dir}}
				code := "open('ok.txt','w').write('guest')\nprint('OK')\n"
				if idx == 3 {
					code = "open('trap.txt','w').write('bad')\nraise ValueError('trap')\n"
				}
				resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
					RawArgs: map[string]any{"code": code},
					CallID:  "call-conc", Actor: "coordinator", View: "primary", Surface: "agent",
					Roots: roots, MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
					Limits: pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
				})
				if err != nil {
					errs <- err.Error()
					return
				}
				if idx < 3 && (!resp.CleanCompletion || resp.ExitCode != 0) {
					errs <- "good concurrent guest failed"
				}
				if idx == 3 && resp.CleanCompletion {
					errs <- "trapping concurrent guest unexpectedly succeeded"
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		var msgs []string
		for m := range errs {
			msgs = append(msgs, m)
		}
		sort.Strings(msgs)
		if len(msgs) > 0 {
			t.Fatalf("concurrent guest failures: %v", msgs)
		}
		// Subsequent guest must succeed cleanly after interleaved trap.
		afterDir := t.TempDir()
		afterRoots := []pythondocuments.HostRootConfig{{ID: "ws-after", Dir: afterDir}}
		after, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			RawArgs: map[string]any{"code": "print('RECOVERED')"},
			CallID:  "call-after", Actor: "coordinator", View: "primary", Surface: "agent",
			Roots: afterRoots, Limits: pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
		})
		contractcheck.FailErr(t, "execute subsequent guest", err)
		if !after.CleanCompletion || strings.TrimSpace(after.Stdout) != "RECOVERED" {
			t.Fatalf("subsequent guest failed after concurrent trap: %+v", after)
		}
	})
}
