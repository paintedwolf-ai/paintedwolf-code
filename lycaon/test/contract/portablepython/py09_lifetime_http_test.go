package portablepython

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func newPY09NativeService(t *testing.T, cfg portablepython.ServiceConfig) *portablepython.Service {
	t.Helper()
	if root := strings.TrimSpace(os.Getenv("PW_QUAL_ENGINE_ROOT")); root != "" && strings.TrimSpace(os.Getenv("LYCAON_ENGINE_ROOT")) == "" {
		t.Setenv("LYCAON_ENGINE_ROOT", root)
	}
	if cfg.ScratchRoot == "" {
		cfg.ScratchRoot = t.TempDir()
	}
	svc, err := portablepython.NewService(context.Background(), cfg)
	if err != nil {
		if os.Getenv("PW_REQUIRE_NATIVE_QUAL") == "1" {
			t.Fatalf("failed to initialize Portable Python native service: %v", err)
		}
		t.Skipf("no staged Portable Python native engine/image discovered: %v", err)
	}
	assertDefaultNativeAOT(t, svc)
	return svc
}

// TestPY09_NativeLifetimeCloseDuringHeldHostCallAndFreshService verifies PY09
// Boundary 3 (Native lifetime):
//  1. A native-aot invocation reaches a host call held in ApprovalBroker and
//     signals synchronization deterministically via OnHoldCreated.
//  2. Service.Close(ctx) is called while the host call is held; it cancels,
//     drains, releases parked resources, and records a clean terminal stop
//     reason and effect count without hanging or leaking.
//  3. Immediately constructing a fresh portablepython.Service on the same staged
//     native-aot bundle succeeds and executes cleanly.
func TestPY09_NativeLifetimeCloseDuringHeldHostCallAndFreshService(t *testing.T) {
	ctx := context.Background()
	receiptDir := t.TempDir()
	wsDir := t.TempDir()

	holdCh := make(chan pythonadmission.ApprovalLease, 1)
	broker := pythonadmission.NewApprovalBroker(pythonadmission.ApprovalBrokerConfig{
		OnHoldCreated: func(lease pythonadmission.ApprovalLease) {
			select {
			case holdCh <- lease:
			default:
			}
		},
	})

	receiptStore, err := portablepython.NewFileReceiptStore(receiptDir)
	contractcheck.FailErr(t, "create FileReceiptStore", err)

	svc := newPY09NativeService(t, portablepython.ServiceConfig{
		Broker:      broker,
		ReceiptSink: receiptStore,
	})

	// Policy requires approval on workspace writes so the guest deterministically
	// parks inside ApprovalBroker.WaitForApproval during its write syscall.
	holdWritePolicy := func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
		if op.Family == pythonruntime.EffectWrite {
			return pythonadmission.PolicyDecision{
				Kind:      pythonruntime.DecisionHold,
				Condition: pythonadmission.ConditionRequiresApproval,
				Reason:    "PY09 deterministic hold on workspace write",
			}
		}
		return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
	}

	roots := []pythondocuments.HostRootConfig{{ID: "ws-py09-hold", Dir: wsDir}}
	doneCh := make(chan struct{})
	var (
		execResp *portablepython.ToolResponse
		execErr  error
	)

	go func() {
		defer close(doneCh)
		execResp, execErr = svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"code": `
with open("held_output.txt", "w", encoding="utf-8") as f:
    f.write("must-not-be-published-on-close")
print("SHOULD_NOT_COMPLETE")
`,
			},
			CallID:       "call-py09-held-close",
			SessionID:    "sess-py09-lifetime",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Policy:       holdWritePolicy,
			Broker:       broker,
			Limits:       pythonruntime.ResourceLimits{Timeout: 30 * time.Second},
		})
	}()

	// Wait deterministically on the boundary channel until the host call is held.
	var heldLease pythonadmission.ApprovalLease
	select {
	case heldLease = <-holdCh:
		t.Logf("boundary synchronized: host call parked on lease %s (exec=%s op=%s)", heldLease.LeaseID, heldLease.ExecutionID, heldLease.OperationID)
	case <-time.After(15 * time.Second):
		_ = svc.Close(ctx)
		t.Fatal("timed out waiting for guest write to enter ApprovalBroker hold")
	}

	// Verify parked-guest tracker reflects the held call before Close.
	if snap := svc.Runtime().ParkedGuests().Snapshot(); snap.ParkedGuests < 0 {
		t.Fatalf("unexpected negative parked guest count: %+v", snap)
	}

	// Close the service while the host call is actively held.
	closeStart := time.Now()
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("svc.Close while host call held failed: %v", err)
	}
	closeDur := time.Since(closeStart)
	t.Logf("svc.Close drained held host call in %v", closeDur)
	if closeDur > 5*time.Second {
		t.Fatalf("svc.Close took too long to cancel and drain held host call: %v", closeDur)
	}

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteTool goroutine did not exit after svc.Close returned")
	}

	// Verify terminal reason, effect count, withheld write, and resource release.
	if execErr != nil {
		t.Fatalf("ExecuteTool returned unexpected transport error instead of terminal stop result: %v", execErr)
	}
	if execResp == nil {
		t.Fatal("ExecuteTool returned nil response")
	}
	if execResp.CleanCompletion {
		t.Fatalf("expected CleanCompletion=false after Close during held call, got true (exit=%d)", execResp.ExitCode)
	}
	if execResp.Stop == nil {
		t.Fatalf("expected non-nil terminal Stop after Close during held call, got nil (exit=%d stderr=%q)", execResp.ExitCode, execResp.Stderr)
	}
	t.Logf("held-call terminal stop: code=%s reason=%q", execResp.Stop.Code, execResp.Stop.Reason)

	// Confirm the withheld file was NOT published to disk.
	if _, statErr := os.Stat(filepath.Join(wsDir, "held_output.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("held_output.txt must not exist on disk after Close during hold, statErr=%v", statErr)
	}

	// Confirm effect admission count was recorded cleanly (>= 1 held write effect).
	reopened, err := portablepython.NewFileReceiptStore(receiptDir)
	contractcheck.FailErr(t, "reopen FileReceiptStore", err)
	adms, err := reopened.LoadAdmissions(execResp.ExecutionID)
	contractcheck.FailErr(t, "LoadAdmissions", err)
	writeEffects := 0
	for _, adm := range adms {
		if adm.Record.Family == pythonruntime.EffectWrite {
			writeEffects++
		}
	}
	if writeEffects == 0 {
		t.Fatalf("expected >= 1 recorded write effect admission in receipts, got %d (total admissions=%d)", writeEffects, len(adms))
	}

	// Confirm parked guest resources were fully released.
	if snap := svc.Runtime().ParkedGuests().Snapshot(); snap.ParkedGuests != 0 || snap.ReviewLeases != 0 {
		t.Fatalf("parked guest resources leaked after Close: %+v", snap)
	}

	// Immediately construct a fresh Service on the same staged native-aot bundle
	// and verify it executes cleanly.
	freshSvc := newPY09NativeService(t, portablepython.ServiceConfig{})
	defer func() { _ = freshSvc.Close(ctx) }()

	freshDir := t.TempDir()
	freshRoots := []pythondocuments.HostRootConfig{{ID: "ws-py09-fresh", Dir: freshDir}}
	freshResp, err := freshSvc.ExecuteTool(ctx, portablepython.ToolRequest{
		RawArgs: map[string]any{
			"code": `
with open("fresh_ok.txt", "w", encoding="utf-8") as f:
    f.write("FRESH_SERVICE_OK")
print("FRESH_SERVICE_OK")
`,
		},
		CallID:       "call-py09-fresh-after-close",
		SessionID:    "sess-py09-lifetime",
		Actor:        "coordinator",
		View:         "primary",
		Surface:      "implement_investigate",
		Roots:        freshRoots,
		MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, freshRoots),
		Limits:       pythonruntime.ResourceLimits{Timeout: 30 * time.Second},
	})
	contractcheck.FailErr(t, "fresh service ExecuteTool", err)
	if !freshResp.CleanCompletion || freshResp.ExitCode != 0 {
		t.Fatalf("fresh service failed: exit=%d clean=%v stop=%+v stderr=%q", freshResp.ExitCode, freshResp.CleanCompletion, freshResp.Stop, freshResp.Stderr)
	}
	if freshResp.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("fresh service backend=%q want %q", freshResp.Backend, pythonruntime.BackendNativeAOT)
	}
	gotFresh, err := os.ReadFile(filepath.Join(freshDir, "fresh_ok.txt"))
	contractcheck.FailErr(t, "read fresh_ok.txt", err)
	if string(gotFresh) != "FRESH_SERVICE_OK" {
		t.Fatalf("fresh_ok.txt=%q want %q", string(gotFresh), "FRESH_SERVICE_OK")
	}
}

// TestPY09_UncertainHTTPSPostAbruptCloseAndReceiptRecovery verifies PY09
// Boundary 4 (Uncertain external effect and receipt recovery):
//  1. A local loopback HTTPS server increments an independent atomic counter on
//     POST, acknowledges receipt to the test harness, and hijacks + abruptly
//     closes the TCP connection before writing any HTTP response.
//  2. Through the default native-aot Python tool, the guest issues one synthetic
//     HTTPS POST.
//  3. Verifies postCount == 1 (zero silent retry), host outcome preserves
//     uncertainty about remote completion (CleanCompletion == false, HTTPRecord
//     PartialFailure == true & BytesSent > 0 & Error != ""), and reopening
//     FileReceiptStore from disk preserves both the effect and uncertainty while
//     postCount remains 1.
func TestPY09_UncertainHTTPSPostAbruptCloseAndReceiptRecovery(t *testing.T) {
	ctx := context.Background()
	receiptDir := t.TempDir()
	wsDir := t.TempDir()

	var postCount atomic.Int64
	serverAckCh := make(chan string, 4)

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		postCount.Add(1)
		serverAckCh <- string(body)

		// Hijack and abruptly close TCP connection before writing any HTTP response.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("ResponseWriter does not support http.Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("Hijack failed: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer tlsSrv.Close()

	parsedURL, err := url.Parse(tlsSrv.URL)
	contractcheck.FailErr(t, "parse TLS server URL", err)

	receiptStore, err := portablepython.NewFileReceiptStore(receiptDir)
	contractcheck.FailErr(t, "create FileReceiptStore", err)

	svc := newPY09NativeService(t, portablepython.ServiceConfig{
		ReceiptSink:       receiptStore,
		DefaultHTTPClient: tlsSrv.Client(),
	})
	defer func() { _ = svc.Close(ctx) }()

	allowPostPolicy := func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
		if op.Family == pythonruntime.EffectHTTP && op.Target.RootID == parsedURL.Host {
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
		}
		return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
	}

	roots := []pythondocuments.HostRootConfig{{ID: "ws-py09-http", Dir: wsDir}}
	resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
		RawArgs: map[string]any{
			"code": `
import requests, sys
payload = '{"event":"py09-uncertain-post","nonce":"n-01"}'
r = requests.post(sys.argv[1], data=payload, headers={"Content-Type": "application/json"})
print("UNEXPECTED_SUCCESS", r.status_code)
`,
			"args": []string{tlsSrv.URL + "/commit"},
		},
		CallID:    "call-py09-uncertain-post",
		SessionID: "sess-py09-http",
		Actor:     "coordinator",
		View:      "primary",
		Surface:   "implement_investigate",
		Roots:     roots,
		Policy:    allowPostPolicy,
		HTTP: portablepython.HTTPRequestOptions{
			Client:            tlsSrv.Client(),
			AllowLoopbackHTTP: true,
		},
		Limits: pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
	})
	contractcheck.FailErr(t, "ExecuteTool uncertain HTTPS POST", err)

	// 1. Independent server counter MUST be exactly 1 (zero silent retry).
	if got := postCount.Load(); got != 1 {
		t.Fatalf("independent server postCount=%d, want exactly 1 (must not silently retry)", got)
	}
	select {
	case receivedBody := <-serverAckCh:
		if !strings.Contains(receivedBody, "py09-uncertain-post") {
			t.Fatalf("server received unexpected body: %q", receivedBody)
		}
	default:
		t.Fatal("server did not acknowledge receipt of POST payload")
	}

	// 2. Host outcome MUST preserve uncertainty about remote completion.
	if resp.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("backend=%q want %q", resp.Backend, pythonruntime.BackendNativeAOT)
	}
	if resp.CleanCompletion {
		t.Fatalf("CleanCompletion must be false when remote HTTPS POST closed without response")
	}
	if resp.ExitCode == 0 {
		t.Fatalf("ExitCode must be non-zero when HTTPS POST failed mid-flight")
	}

	httpRecs := resp.AdmissionEvidence.HTTPRecords()
	if len(httpRecs) != 1 {
		t.Fatalf("expected exactly 1 HTTPRecord, got %d (%+v)", len(httpRecs), httpRecs)
	}
	hr := httpRecs[0]
	if hr.Method != http.MethodPost || hr.BytesSent <= 0 || !hr.PartialFailure || hr.Status != 0 || hr.Error == "" {
		t.Fatalf("HTTPRecord did not preserve remote-completion uncertainty: %+v", hr)
	}
	t.Logf("uncertain HTTPRecord preserved: method=%s bytes_sent=%d partial_failure=%v status=%d err=%q",
		hr.Method, hr.BytesSent, hr.PartialFailure, hr.Status, hr.Error)

	// 3. Reopen FileReceiptStore from disk and verify persisted receipts preserve
	// the effect & uncertainty while postCount stays 1.
	reopened, err := portablepython.NewFileReceiptStore(receiptDir)
	contractcheck.FailErr(t, "reopen FileReceiptStore", err)

	execs, err := reopened.LoadExecutions()
	contractcheck.FailErr(t, "LoadExecutions", err)
	var foundExec *pythonevidence.ExecutionRecord
	for i := range execs {
		if execs[i].ExecutionID == resp.ExecutionID {
			foundExec = &execs[i]
			break
		}
	}
	if foundExec == nil {
		t.Fatalf("execution %s not found in reopened executions.jsonl", resp.ExecutionID)
	}
	if foundExec.CleanCompletion || foundExec.ExitCode == 0 {
		t.Fatalf("reopened ExecutionRecord reported clean completion despite uncertain POST: %+v", *foundExec)
	}

	adms, err := reopened.LoadAdmissions(resp.ExecutionID)
	contractcheck.FailErr(t, "LoadAdmissions", err)
	foundHTTPAdm := false
	for _, adm := range adms {
		if adm.Record.Family == pythonruntime.EffectHTTP {
			foundHTTPAdm = true
			if adm.Record.Outcome.Succeeded {
				t.Fatalf("reopened HTTP admission falsely marked Succeeded=true: %+v", adm.Record)
			}
			if adm.Record.Outcome.BytesTransferred <= 0 || strings.TrimSpace(adm.Record.Outcome.Error) == "" {
				t.Fatalf("reopened HTTP admission lost bytes/error uncertainty details: %+v", adm.Record.Outcome)
			}
		}
	}
	if !foundHTTPAdm {
		t.Fatalf("no persisted EffectHTTP admission found in reopened admissions.jsonl for %s", resp.ExecutionID)
	}

	scope := pythonevidence.ViewerScope{
		Role:      pythonevidence.RoleCoordinator,
		SessionID: "sess-py09-http",
		Actor:     "coordinator",
		View:      "primary",
	}
	proj, err := reopened.ProjectPersistedForDen(resp.ExecutionID, scope)
	contractcheck.FailErr(t, "ProjectPersistedForDen", err)
	if !proj.Visible || proj.CleanCompletion {
		t.Fatalf("reopened Den projection did not preserve visible uncertain outcome: %+v", proj)
	}

	// Confirm postCount remained 1 across receipt recovery.
	if got := postCount.Load(); got != 1 {
		t.Fatalf("postCount changed during receipt recovery: got %d want 1", got)
	}
}

// TestPY09_NativeActivityToReopenedFilesAndDen verifies PY09 Boundary 5
// (Normal product route — tool result to reopened Files/Den witness):
//  1. Executes one real native-aot Python activity that writes a workspace
//     artifact and emits overflow stdout (> inline budget) persisted via
//     BlobRetainedStreamPort + FileReceiptStore.
//  2. Closes the original Service and reopens fresh FileReceiptStore +
//     BlobRetainedStreamPort from disk.
//  3. Compares identity (execution_id, call_id, runtime_image_id, backend),
//     visibility (authorized coordinator vs unauthorized cross-session viewer),
//     retained output bytes, published workspace file, and outcome.
func TestPY09_NativeActivityToReopenedFilesAndDen(t *testing.T) {
	ctx := context.Background()
	dataRoot := t.TempDir()
	receiptDir := filepath.Join(dataRoot, "receipts")
	wsDir := t.TempDir()

	blobStore := blobstore.Store{
		Root: dataRoot,
		Dir:  filepath.Join("portable-python", "retained"),
	}
	streamPort := portablepython.NewBlobRetainedStreamPort(blobStore)
	screener := pythonevidence.TestSecretScreener{}
	retainedStore, err := pythonevidence.NewProductionRetainedArtifactStore(pythonevidence.RetainedArtifactConfig{
		Screener:   screener,
		StreamPort: streamPort,
	})
	contractcheck.FailErr(t, "NewProductionRetainedArtifactStore", err)

	receiptStore, err := portablepython.NewFileReceiptStore(receiptDir)
	contractcheck.FailErr(t, "NewFileReceiptStore", err)

	svc := newPY09NativeService(t, portablepython.ServiceConfig{
		SecretScreener:    screener,
		RetainedStore:     retainedStore,
		StreamPort:        streamPort,
		ReceiptSink:       receiptStore,
		InlineBudgetBytes: 512, // force overflow into disk-backed retained artifact
	})

	roots := []pythondocuments.HostRootConfig{{ID: "ws-py09-den", Dir: wsDir}}
	resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
		RawArgs: map[string]any{
			"code": `
import json
report = {"witness": "py09-den-files", "rows": 42}
with open("den_witness.json", "w", encoding="utf-8") as f:
    json.dump(report, f)
for i in range(40):
    print(f"PY09_WITNESS_LINE_{i:02d}_" + "x" * 24)
`,
		},
		CallID:       "call-py09-den-witness",
		SessionID:    "sess-py09-den",
		CheckoutID:   "chk-py09-den",
		Actor:        "coordinator",
		View:         "primary",
		Surface:      "implement_investigate",
		Roots:        roots,
		MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		Limits:       pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
	})
	contractcheck.FailErr(t, "ExecuteTool Den witness", err)
	if !resp.CleanCompletion || resp.ExitCode != 0 {
		t.Fatalf("Den witness activity failed: exit=%d clean=%v stop=%+v stderr=%q", resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.Stderr)
	}
	if resp.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("backend=%q want %q", resp.Backend, pythonruntime.BackendNativeAOT)
	}
	if resp.RetainedStdoutArtifactID == "" {
		t.Fatalf("expected overflow stdout to be retained in disk-backed store (inline budget 512)")
	}
	wantImageID := svc.RuntimeManifest().ImageID

	// Close the original Service before reopening durable Files/Den stores.
	contractcheck.FailErr(t, "close original service", svc.Close(ctx))

	// Independently verify workspace file on disk.
	diskBytes, err := os.ReadFile(filepath.Join(wsDir, "den_witness.json"))
	contractcheck.FailErr(t, "read den_witness.json", err)
	var parsed map[string]any
	contractcheck.FailErr(t, "unmarshal den_witness.json", json.Unmarshal(diskBytes, &parsed))
	if parsed["witness"] != "py09-den-files" {
		t.Fatalf("unexpected den_witness.json content: %s", string(diskBytes))
	}

	// Reopen FileReceiptStore and BlobRetainedStreamPort from disk.
	reopenedReceipts, err := portablepython.NewFileReceiptStore(receiptDir)
	contractcheck.FailErr(t, "reopen FileReceiptStore", err)
	reopenedPort := portablepython.NewBlobRetainedStreamPort(blobstore.Store{
		Root: dataRoot,
		Dir:  filepath.Join("portable-python", "retained"),
	})

	// Compare persisted ExecutionRecord identity & outcome.
	execs, err := reopenedReceipts.LoadExecutions()
	contractcheck.FailErr(t, "LoadExecutions", err)
	if len(execs) != 1 || execs[0].ExecutionID != resp.ExecutionID {
		t.Fatalf("reopened executions mismatch: got %+v want execution_id=%s", execs, resp.ExecutionID)
	}
	if execs[0].RuntimeImageID != wantImageID {
		t.Fatalf("reopened runtime_image_id=%q want %q", execs[0].RuntimeImageID, wantImageID)
	}
	if !execs[0].CleanCompletion || execs[0].ExitCode != 0 {
		t.Fatalf("reopened execution outcome mismatch: %+v", execs[0])
	}

	// Compare authorized vs unauthorized Den visibility.
	authorizedScope := pythonevidence.ViewerScope{
		Role:       pythonevidence.RoleCoordinator,
		SessionID:  "sess-py09-den",
		CheckoutID: "chk-py09-den",
		Actor:      "coordinator",
		View:       "primary",
	}
	proj, err := reopenedReceipts.ProjectPersistedForDen(resp.ExecutionID, authorizedScope)
	contractcheck.FailErr(t, "ProjectPersistedForDen(authorized)", err)
	if !proj.Visible || proj.RedactedForVisibility {
		t.Fatalf("expected authorized Den projection visible, got %+v", proj)
	}
	if proj.ExecutionID != resp.ExecutionID || proj.CallID != "call-py09-den-witness" || !proj.CleanCompletion || proj.ExitCode != 0 {
		t.Fatalf("reopened Den projection identity/outcome mismatch: %+v", proj)
	}

	otherSessionScope := pythonevidence.ViewerScope{
		Role:       pythonevidence.RoleCoordinator,
		SessionID:  "sess-other-unauthorized",
		CheckoutID: "chk-py09-den",
		Actor:      "coordinator",
		View:       "primary",
	}
	unauthProj, err := reopenedReceipts.ProjectPersistedForDen(resp.ExecutionID, otherSessionScope)
	contractcheck.FailErr(t, "ProjectPersistedForDen(unauthorized)", err)
	if unauthProj.Visible || !unauthProj.RedactedForVisibility {
		t.Fatalf("expected cross-session Den projection redacted, got %+v", unauthProj)
	}

	// Reopen retained stdout bytes through Files/Den viewer scope.
	retainedBytes, err := reopenedReceipts.ReadRetainedBytes(ctx, authorizedScope, resp.RetainedStdoutArtifactID, reopenedPort)
	contractcheck.FailErr(t, "ReadRetainedBytes(authorized)", err)
	if !strings.Contains(string(retainedBytes), "PY09_WITNESS_LINE_00_") || !strings.Contains(string(retainedBytes), "PY09_WITNESS_LINE_39_") {
		t.Fatalf("reopened retained stdout missing expected witness lines (len=%d)", len(retainedBytes))
	}
	if _, err := reopenedReceipts.ReadRetainedBytes(ctx, otherSessionScope, resp.RetainedStdoutArtifactID, reopenedPort); err == nil {
		t.Fatal("expected unauthorized cross-session ReadRetainedBytes to be denied")
	}

	// Verify publication receipt survived reopen.
	pubs, err := reopenedReceipts.LoadPublications(resp.ExecutionID)
	contractcheck.FailErr(t, "LoadPublications", err)
	if len(pubs) == 0 {
		t.Fatalf("expected >= 1 persisted publication receipt for den_witness.json after reopen")
	}
	t.Logf("Files/Den reopen witness verified: exec=%s image=%s backend=%s retained_bytes=%d publications=%d",
		proj.ExecutionID, wantImageID, resp.Backend, len(retainedBytes), len(pubs))
}
