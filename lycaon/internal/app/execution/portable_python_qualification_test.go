package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

// countingListener wraps a net.Listener and counts raw TCP accepts so denied
// destinations prove zero TCP connections (not just zero HTTP handler hits).
type countingListener struct {
	net.Listener
	accepts atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return c, err
}

func newCountingTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, *countingListener) {
	t.Helper()
	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen tcp 127.0.0.1:0", err)
	cl := &countingListener{Listener: rawLn}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = cl
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, cl
}

// buildQualificationRuntime constructs the real app execution.Build runtime.
// When LYCAON_ENGINE_ROOT already contains a staged distribution (from
// scripts/stage-engine.sh) and PW_PORTABLE_PYTHON_IMAGE_DIR is unset, it uses
// that real staged distribution directly with zero test symlink helpers.
func buildQualificationRuntime(t *testing.T, cfgDir string) (execution.Runtime, *settings.ApprovalStore) {
	t.Helper()
	existingEngineRoot := strings.TrimSpace(os.Getenv(configlayout.EnvEngineRoot))
	hasStagedDist := existingEngineRoot != "" && fileExists(filepath.Join(existingEngineRoot, "portable-python", "image", "image-manifest.json"))
	if !hasStagedDist {
		sourceImageDir := strings.TrimSpace(os.Getenv(portablepython.EnvImageDir))
		if sourceImageDir == "" {
			t.Skip("neither staged LYCAON_ENGINE_ROOT nor PW_PORTABLE_PYTHON_IMAGE_DIR is set; skipping qualification test")
		}
		engineRoot := t.TempDir()
		stagedParent := filepath.Join(engineRoot, "portable-python")
		testutil.FailErr(t, "mkdir staged engine-root", os.MkdirAll(stagedParent, 0o755))
		testutil.FailErr(t, "symlink verified image", os.Symlink(sourceImageDir, filepath.Join(stagedParent, "image")))
		t.Setenv(configlayout.EnvEngineRoot, engineRoot)
	}
	t.Setenv(configdir.EnvConfigDir, cfgDir)
	t.Setenv(portablepython.EnvImageDir, "")

	approvalStore, err := settings.NewApprovalStoreAt(filepath.Join(cfgDir, "approvals.yaml"))
	testutil.FailErr(t, "create persisted approval store", err)
	testutil.FailErr(t, "configure strict posture", approvalStore.PutGlobal(settings.ApprovalConfig{
		Posture: gate.PostureStrict,
	}))

	catalog := extpackstest.StockCatalog(t)
	rt, err := execution.Build(&settings.Service{Approvals: approvalStore}, "", catalog, decide.Reranker{}, nil)
	testutil.FailErr(t, "execution.Build qualification runtime", err)
	if rt.Host.Commands.PortablePython() == nil {
		t.Fatal("expected execution.Build to auto-wire portablepython.Service")
	}
	return rt, approvalStore
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// TestPortablePythonFinalQualification covers the coordinator's final product
// qualification requirements on top of the real execution.Build composition root.
func TestPortablePythonFinalQualification(t *testing.T) {
	ctx := context.Background()

	// 1. TCP-level zero-connection verification, cross-recipient credential
	// isolation, and partial streamed delivery.
	t.Run("tcp zero-accept denied connections, cross-recipient credential isolation, and partial stream", func(t *testing.T) {
		cfgDir := t.TempDir()
		rt, _ := buildQualificationRuntime(t, cfgDir)
		svc := rt.Host.Commands.PortablePython()
		defer func() { _ = svc.Close(ctx) }()

		// 1a. Direct denied HTTPS destination -> tcpAccepts == 0.
		deniedSrv, deniedLn := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		respDirectDeny, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID: "qual-tcp-direct-deny",
			CallID:      "call-qual-tcp-direct-deny",
			SessionID:   "sess-qual-tcp",
			Actor:       "implement",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `import requests, sys; requests.get(sys.argv[1])`,
			Args:        []string{deniedSrv.URL + "/forbidden"},
			HTTPClient:  deniedSrv.Client(),
			Policy: func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
				if op.Family == pythonruntime.EffectHTTP {
					return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionStop, Condition: pythonadmission.ConditionEffectDenied, Reason: "denied destination"}
				}
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
			},
		})
		testutil.FailErr(t, "execute direct denied HTTPS", err)
		if respDirectDeny.CleanCompletion || respDirectDeny.Stop == nil || respDirectDeny.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("direct deny = %+v, want COMMAND_EFFECT_NOT_ALLOWED", respDirectDeny)
		}
		if got := deniedLn.accepts.Load(); got != 0 {
			t.Fatalf("denied server observed %d raw TCP accepts, want 0", got)
		}

		// 1b. Denied redirect target (A -> B) -> B has tcpAccepts == 0.
		redirTargetSrv, redirTargetLn := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		redirTargetURL, err := url.Parse(redirTargetSrv.URL)
		testutil.FailErr(t, "parse redirTarget URL", err)
		redirEntrySrv, _ := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirTargetSrv.URL+"/secret-hop", http.StatusFound)
		}))
		respRedirDeny, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID: "qual-tcp-redir-deny",
			CallID:      "call-qual-tcp-redir-deny",
			SessionID:   "sess-qual-tcp",
			Actor:       "implement",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `import requests, sys; requests.get(sys.argv[1])`,
			Args:        []string{redirEntrySrv.URL + "/start"},
			HTTPClient:  redirEntrySrv.Client(),
			Policy: func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
				if op.Family == pythonruntime.EffectHTTP && op.Target.RootID == redirTargetURL.Host {
					return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionStop, Condition: pythonadmission.ConditionEffectDenied, Reason: "redirect target denied"}
				}
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
			},
		})
		testutil.FailErr(t, "execute denied redirect HTTPS", err)
		if respRedirDeny.CleanCompletion || respRedirDeny.Stop == nil || respRedirDeny.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("redirect deny = %+v, want COMMAND_EFFECT_NOT_ALLOWED", respRedirDeny)
		}
		if got := redirTargetLn.accepts.Load(); got != 0 {
			t.Fatalf("denied redirect target observed %d raw TCP accepts, want 0", got)
		}

		// 1c. Cross-recipient credential isolation: Bearer token allowed ONLY for Recipient A, never Recipient B.
		var recAGotAuth, recBGotAuth string
		recBSrv, recBLn := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recBGotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte("recipient-b"))
		}))
		recASrv, _ := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recAGotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte("recipient-a-ok"))
		}))
		recAURL, _ := url.Parse(recASrv.URL)
		scopedScreener := func(u *url.URL, h http.Header) error {
			if h.Get("Authorization") != "" && u.Host != recAURL.Host {
				return errors.New("credential scoped exclusively to recipient A")
			}
			return nil
		}
		allowAll := func(_ context.Context, _ pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
		}

		respA, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID:        "qual-cred-a",
			CallID:             "call-qual-cred-a",
			SessionID:          "sess-qual-cred",
			Actor:              "implement",
			View:               "workspace",
			Mode:               pythonruntime.ModeCode,
			Code:               `import requests, sys; r = requests.get(sys.argv[1], headers={"Authorization": "Bearer secret-for-a"}); print(r.text)`,
			Args:               []string{recASrv.URL + "/api"},
			HTTPClient:         recASrv.Client(),
			CredentialScreener: scopedScreener,
			Policy:             allowAll,
		})
		testutil.FailErr(t, "execute credentialed request to recipient A", err)
		if !respA.CleanCompletion || strings.TrimSpace(respA.Stdout) != "recipient-a-ok" || recAGotAuth != "Bearer secret-for-a" {
			t.Fatalf("recipient A mismatch: resp=%+v auth=%q", respA, recAGotAuth)
		}

		respB, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID:        "qual-cred-b",
			CallID:             "call-qual-cred-b",
			SessionID:          "sess-qual-cred",
			Actor:              "implement",
			View:               "workspace",
			Mode:               pythonruntime.ModeCode,
			Code:               `import requests, sys; requests.get(sys.argv[1], headers={"Authorization": "Bearer secret-for-a"})`,
			Args:               []string{recBSrv.URL + "/steal"},
			HTTPClient:         recBSrv.Client(),
			CredentialScreener: scopedScreener,
			Policy:             allowAll,
		})
		testutil.FailErr(t, "execute cross-recipient credential request to B", err)
		if respB.CleanCompletion || recBGotAuth != "" || recBLn.accepts.Load() != 0 {
			t.Fatalf("recipient B leaked credential or connected: clean=%v auth=%q tcpAccepts=%d", respB.CleanCompletion, recBGotAuth, recBLn.accepts.Load())
		}

		// 1d. Partial streamed delivery interrupted mid-stream survives reopen in persisted receipts.
		chunkSent := make(chan struct{})
		releaseStream := make(chan struct{})
		streamSrv, _ := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("partial-chunk-1\n"))
			if flusher != nil {
				flusher.Flush()
			}
			close(chunkSent)
			select {
			case <-releaseStream:
			case <-r.Context().Done():
			}
		}))
		defer close(releaseStream)

		streamCtx, cancelStream := context.WithCancel(ctx)
		go func() {
			<-chunkSent
			time.Sleep(150 * time.Millisecond)
			cancelStream()
		}()
		respStream, err := svc.ExecuteTool(streamCtx, portablepython.ToolRequest{
			ExecutionID: "qual-partial-stream",
			CallID:      "call-qual-partial-stream",
			SessionID:   "sess-qual-stream",
			Actor:       "implement",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `import requests, sys; r = requests.get(sys.argv[1], stream=True); print(next(r.iter_lines(decode_unicode=True)), flush=True); r.raw.read()`,
			Args:        []string{streamSrv.URL + "/stream"},
			HTTPClient:  streamSrv.Client(),
			Policy:      allowAll,
		})
		testutil.FailErr(t, "execute partial stream", err)
		if respStream.CleanCompletion {
			t.Fatalf("expected interrupted stream to not cleanly complete: %+v", respStream)
		}
		// Reopen persisted receipt store from disk and verify the interrupted stream execution was recorded.
		reopenedReceipts, err := portablepython.NewFileReceiptStore(filepath.Join(cfgDir, "portable-python", "receipts"))
		testutil.FailErr(t, "reopen FileReceiptStore after partial stream", err)
		adms, err := reopenedReceipts.LoadAdmissions("qual-partial-stream")
		testutil.FailErr(t, "load persisted admissions for partial stream", err)
		sawHTTP := false
		for _, a := range adms {
			if a.Record.Family == pythonruntime.EffectHTTP {
				sawHTTP = true
			}
		}
		if !sawHTTP {
			t.Fatalf("reopened receipt store missing HTTP admission for partial stream: %+v", adms)
		}
	})

	// 2. Durable persisted approvals across Approval (dedup), Decline, Cancel, and Restart (reopen).
	t.Run("durable persisted approvals across approve, decline, cancel, and host restart", func(t *testing.T) {
		cfgDir := t.TempDir()
		rt1, _ := buildQualificationRuntime(t, cfgDir)
		svc1 := rt1.Host.Commands.PortablePython()
		broker1 := svc1.Broker()
		if broker1 == nil {
			t.Fatal("expected execution.Build to wire durable ApprovalBroker on Service")
		}

		var bodyCount atomic.Int32
		srv, srvLn := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyCount.Add(1)
			b, _ := io.ReadAll(r.Body)
			_, _ = w.Write([]byte("OK:" + string(b)))
		}))
		holdPolicy := func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
			if op.Family == pythonruntime.EffectHTTP {
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionHold, Reason: "human approval required"}
			}
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
		}

		waitForPendingLease := func(b *pythonadmission.ApprovalBroker, execID string) pythonadmission.ApprovalLease {
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				for _, l := range b.Registry().List() {
					if l.ExecutionID == execID && l.State == pythonadmission.LeasePending {
						return l
					}
				}
				time.Sleep(15 * time.Millisecond)
			}
			t.Fatalf("timed out waiting for pending lease for %s", execID)
			return pythonadmission.ApprovalLease{}
		}

		// 2a. Approve once (and repeat same approval -> error, still 1 body).
		var approvedLeaseID string
		approveDone := make(chan struct{})
		go func() {
			defer close(approveDone)
			l := waitForPendingLease(broker1, "qual-hold-approve")
			approvedLeaseID = l.LeaseID
			_ = broker1.Approve(l.LeaseID, "approved once")
		}()
		respApp, err := svc1.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID: "qual-hold-approve",
			CallID:      "call-qual-hold-approve",
			SessionID:   "sess-qual-hold",
			Actor:       "implement",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `import requests, sys; r = requests.post(sys.argv[1], data="once"); print(r.text)`,
			Args:        []string{srv.URL + "/approve"},
			HTTPClient:  srv.Client(),
			Policy:      holdPolicy,
		})
		<-approveDone
		testutil.FailErr(t, "held approve execution", err)
		if !respApp.CleanCompletion || strings.TrimSpace(respApp.Stdout) != "OK:once" || bodyCount.Load() != 1 {
			t.Fatalf("approve once mismatch: resp=%+v stop=%+v bodies=%d", respApp, respApp.Stop, bodyCount.Load())
		}
		if dupErr := broker1.Approve(approvedLeaseID, "duplicate approve"); dupErr == nil {
			t.Fatal("expected duplicate Approve on consumed lease to fail")
		}
		if bodyCount.Load() != 1 {
			t.Fatalf("duplicate approval caused extra body delivery: %d", bodyCount.Load())
		}

		// 2b. Decline -> 0 additional bodies, 0 additional TCP accepts.
		acceptsBeforeDecline := srvLn.accepts.Load()
		declineDone := make(chan struct{})
		go func() {
			defer close(declineDone)
			l := waitForPendingLease(broker1, "qual-hold-decline")
			_ = broker1.Decline(l.LeaseID, "declined")
		}()
		respDec, err := svc1.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID: "qual-hold-decline",
			CallID:      "call-qual-hold-decline",
			SessionID:   "sess-qual-hold",
			Actor:       "implement",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `import requests, sys; requests.post(sys.argv[1], data="declined")`,
			Args:        []string{srv.URL + "/decline"},
			HTTPClient:  srv.Client(),
			Policy:      holdPolicy,
		})
		<-declineDone
		testutil.FailErr(t, "held decline execution", err)
		if respDec.CleanCompletion || respDec.Stop == nil || respDec.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("decline = %+v, want COMMAND_EFFECT_NOT_ALLOWED", respDec)
		}
		if bodyCount.Load() != 1 || srvLn.accepts.Load() != acceptsBeforeDecline {
			t.Fatalf("declined POST connected: bodies=%d accepts=%d (before=%d)", bodyCount.Load(), srvLn.accepts.Load(), acceptsBeforeDecline)
		}

		// 2c. Cancel / Stop while held -> 0 additional bodies, 0 additional TCP accepts.
		acceptsBeforeCancel := srvLn.accepts.Load()
		cancelCtx, cancelHold := context.WithCancel(ctx)
		cancelDone := make(chan struct{})
		go func() {
			defer close(cancelDone)
			_ = waitForPendingLease(broker1, "qual-hold-cancel")
			cancelHold()
		}()
		respCan, err := svc1.ExecuteTool(cancelCtx, portablepython.ToolRequest{
			ExecutionID: "qual-hold-cancel",
			CallID:      "call-qual-hold-cancel",
			SessionID:   "sess-qual-hold",
			Actor:       "implement",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code:        `import requests, sys; requests.post(sys.argv[1], data="cancelled")`,
			Args:        []string{srv.URL + "/cancel"},
			HTTPClient:  srv.Client(),
			Policy:      holdPolicy,
		})
		<-cancelDone
		testutil.FailErr(t, "held cancel execution", err)
		if respCan.CleanCompletion || bodyCount.Load() != 1 || srvLn.accepts.Load() != acceptsBeforeCancel {
			t.Fatalf("cancelled hold unexpected state: clean=%v bodies=%d accepts=%d", respCan.CleanCompletion, bodyCount.Load(), srvLn.accepts.Load())
		}

		// 2d. Host restart while held -> close rt1/svc1, reopen rt2 from disk ->
		// startup ReconcileOnStartup settles lost lease; stale approval rejected; 0 bodies delivered!
		acceptsBeforeRestart := srvLn.accepts.Load()
		restartCtx, cancelRestart := context.WithCancel(ctx)
		restartExecDone := make(chan struct{})
		go func() {
			defer close(restartExecDone)
			_, _ = svc1.ExecuteTool(restartCtx, portablepython.ToolRequest{
				ExecutionID: "qual-hold-restart",
				CallID:      "call-qual-hold-restart",
				SessionID:   "sess-qual-hold",
				Actor:       "implement",
				View:        "workspace",
				Mode:        pythonruntime.ModeCode,
				Code:        `import requests, sys; requests.post(sys.argv[1], data="lost-on-restart")`,
				Args:        []string{srv.URL + "/restart"},
				HTTPClient:  srv.Client(),
				Policy:      holdPolicy,
			})
		}()
		lostLease := waitForPendingLease(broker1, "qual-hold-restart")
		// Simulate host process restart by constructing a fresh execution.Build against the same cfgDir BEFORE cancelling old goroutine.
		rt2, _ := buildQualificationRuntime(t, cfgDir)
		svc2 := rt2.Host.Commands.PortablePython()
		defer func() { _ = svc2.Close(ctx) }()
		cancelRestart()
		<-restartExecDone
		_ = svc1.Close(ctx)

		broker2 := svc2.Broker()
		reconciled, ok := broker2.Registry().Get(lostLease.LeaseID)
		if !ok || reconciled.State != pythonadmission.LeaseLostOnRestart {
			t.Fatalf("expected lease %s to be LeaseLostOnRestart after restart reopen, got ok=%v lease=%+v", lostLease.LeaseID, ok, reconciled)
		}
		if staleErr := broker2.Approve(lostLease.LeaseID, "stale approval after restart"); !errors.Is(staleErr, pythonadmission.ErrStaleApprovalAfterRestart) {
			t.Fatalf("stale approval err = %v, want ErrStaleApprovalAfterRestart", staleErr)
		}
		if bodyCount.Load() != 1 || srvLn.accepts.Load() != acceptsBeforeRestart {
			t.Fatalf("restarted/stale approval delivered request: bodies=%d accepts=%d", bodyCount.Load(), srvLn.accepts.Load())
		}
	})

	// 3. PY05 Files/Den/Session Reopen Binding & Cross-Session/Worker Isolation.
	t.Run("Files/Den/session reopen and cross-session/worker isolation", func(t *testing.T) {
		cfgDir := t.TempDir()
		rt1, _ := buildQualificationRuntime(t, cfgDir)
		wsDir := t.TempDir()
		testutil.FailErr(t, "seed input.txt", os.WriteFile(filepath.Join(wsDir, "input.txt"), []byte("hello-qual\n"), 0o644))

		// Execute read + write + overflow stdout through the real registered tool.
		writeResp := runInstalledPython(t, ctx, rt1, map[string]any{
			"code": `txt = open("input.txt").read().strip(); open("out.txt", "w").write(txt + "-written\n"); print("Q" * 80000)`,
		}, workspaceTC("sess-reopen-a", "implement", "", wsDir))
		if !writeResp.CleanCompletion || !writeResp.StdoutTruncated || writeResp.RetainedStdoutArtifactID == "" {
			t.Fatalf("write+overflow run failed: %+v", writeResp)
		}

		// Execute a denied protected write in the same session.
		denyResp := runInstalledPython(t, ctx, rt1, map[string]any{
			"code": `open(".git/config", "w").write("bad")`,
		}, workspaceTC("sess-reopen-a", "implement", "", wsDir))
		if denyResp.CleanCompletion || denyResp.Stop == nil || denyResp.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("expected protected write refusal, got %+v", denyResp)
		}

		// Close the first runtime completely and reopen fresh receipt + blob stores from disk.
		_ = rt1.Host.Commands.PortablePython().Close(ctx)

		reopenedReceipts, err := portablepython.NewFileReceiptStore(filepath.Join(cfgDir, "portable-python", "receipts"))
		testutil.FailErr(t, "reopen FileReceiptStore", err)
		reopenedPort := portablepython.NewBlobRetainedStreamPort(blobstore.Store{
			Root: cfgDir,
			Dir:  filepath.Join("portable-python", "retained"),
		})

		// Authorized coordinator scope sees persisted publications and reads disk-backed retained output.
		authScope := pythonevidence.ViewerScope{
			Role:      pythonevidence.RoleCoordinator,
			SessionID: "sess-reopen-a",
			Actor:     "implement",
			View:      string(workspaceTC("sess-reopen-a", "implement", "", wsDir).Source.SourceWorkspaceKind),
		}
		projWrite, err := reopenedReceipts.ProjectPersistedForDen(writeResp.ExecutionID, authScope)
		testutil.FailErr(t, "project persisted write execution for Den", err)
		if !projWrite.Visible || len(projWrite.PublishedDiffs) == 0 {
			t.Fatalf("expected visible projection with published diffs after reopen, got %+v", projWrite)
		}
		if projWrite.PublishedDiffs[0].AttributionKind != pythonevidence.AttributionPythonWorkspacePublication {
			t.Fatalf("unexpected attribution kind after reopen: %+v", projWrite.PublishedDiffs[0])
		}

		projDeny, err := reopenedReceipts.ProjectPersistedForDen(denyResp.ExecutionID, authScope)
		testutil.FailErr(t, "project persisted deny execution for Den", err)
		if !projDeny.Visible || len(projDeny.RefusedChildren) == 0 {
			t.Fatalf("expected visible projection with refused children after reopen, got %+v", projDeny)
		}

		retainedBytes, err := reopenedReceipts.ReadRetainedBytes(ctx, authScope, writeResp.RetainedStdoutArtifactID, reopenedPort)
		testutil.FailErr(t, "read persisted retained stdout bytes after reopen", err)
		if len(retainedBytes) < 80000 {
			t.Fatalf("reopened retained bytes = %d, want >= 80000", len(retainedBytes))
		}

		// Cross-session and cross-worker scopes MUST be denied on both Den projection and retained output!
		otherSessionScope := pythonevidence.ViewerScope{
			Role:      pythonevidence.RoleCoordinator,
			SessionID: "sess-other-b",
			Actor:     "implement",
			View:      authScope.View,
		}
		otherWorkerScope := pythonevidence.ViewerScope{
			Role:      pythonevidence.RoleWorker,
			SessionID: "sess-reopen-a",
			Actor:     "other-worker",
			View:      authScope.View,
		}
		for _, deniedScope := range []pythonevidence.ViewerScope{otherSessionScope, otherWorkerScope} {
			projDenied, err := reopenedReceipts.ProjectPersistedForDen(writeResp.ExecutionID, deniedScope)
			testutil.FailErr(t, "project for denied scope", err)
			if projDenied.Visible || !projDenied.RedactedForVisibility {
				t.Fatalf("denied scope %+v unexpectedly saw projection: %+v", deniedScope, projDenied)
			}
			if _, rErr := reopenedReceipts.ReadRetainedBytes(ctx, deniedScope, writeResp.RetainedStdoutArtifactID, reopenedPort); !errors.Is(rErr, pythonevidence.ErrAccessDenied) {
				t.Fatalf("denied scope %+v retained read err = %v, want ErrAccessDenied", deniedScope, rErr)
			}
		}
	})

	// 4. Actual project-runner distinct interpreter identity (host python3 vs Portable Python WASI).
	t.Run("distinct interpreter identity between Portable Python WASI and host project runner", func(t *testing.T) {
		cfgDir := t.TempDir()
		rt, _ := buildQualificationRuntime(t, cfgDir)
		defer func() { _ = rt.Host.Commands.PortablePython().Close(ctx) }()

		wsDir := t.TempDir()
		wasiResp := runInstalledPython(t, ctx, rt, map[string]any{
			"code": `import json, sys; print(json.dumps({"platform": sys.platform, "executable": sys.executable}))`,
		}, workspaceTC("sess-interp-compare", "implement", "", wsDir))
		if !wasiResp.CleanCompletion || wasiResp.ExitCode != 0 {
			t.Fatalf("portable python interpreter probe failed: %+v", wasiResp)
		}
		var wasiID struct {
			Platform   string `json:"platform"`
			Executable string `json:"executable"`
		}
		testutil.FailErr(t, "decode wasi interpreter identity", json.Unmarshal([]byte(strings.TrimSpace(wasiResp.Stdout)), &wasiID))
		if wasiID.Platform != "wasi" {
			t.Fatalf("expected Portable Python platform=wasi, got %+v", wasiID)
		}

		hostPy, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("host python3 not on PATH; skipping host-vs-WASI distinct interpreter comparison")
		}
		hostOut, err := exec.Command(hostPy, "-c", `import json, sys; print(json.dumps({"platform": sys.platform, "executable": sys.executable}))`).CombinedOutput()
		testutil.FailErr(t, "run host python3 interpreter probe", err)
		var hostID struct {
			Platform   string `json:"platform"`
			Executable string `json:"executable"`
		}
		testutil.FailErr(t, "decode host interpreter identity", json.Unmarshal([]byte(strings.TrimSpace(string(hostOut))), &hostID))
		if hostID.Platform == wasiID.Platform || hostID.Executable == wasiID.Executable {
			t.Fatalf("expected distinct interpreter identities, got wasi=%+v host=%+v (GOOS=%s)", wasiID, hostID, runtime.GOOS)
		}
		t.Logf("verified distinct interpreter identity: portable=%+v vs project_runner=%+v", wasiID, hostID)
	})
}
