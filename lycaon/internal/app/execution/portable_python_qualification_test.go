package execution_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
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

func combinedTLSClient(srvs ...*httptest.Server) *http.Client {
	pool := x509.NewCertPool()
	for _, s := range srvs {
		if s != nil && s.Certificate() != nil {
			pool.AddCert(s.Certificate())
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    pool,
			},
		},
	}
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
			HTTP:        portablepython.HTTPRequestOptions{Client: deniedSrv.Client()},
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
			HTTP:        portablepython.HTTPRequestOptions{Client: redirEntrySrv.Client()},
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

		// 1c. Allowed cross-origin redirect to second authorized recipient (A -> B)
		// strips credential scoped solely to A while B actually receives the request.
		var recAGotAuth, recBGotAuth string
		var recBHits atomic.Int64
		recBSrv, recBLn := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recBHits.Add(1)
			recBGotAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("redirected-b-ok"))
		}))
		recASrv, _ := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			recAGotAuth = r.Header.Get("Authorization")
			if r.URL.Path == "/redirect-to-b" {
				http.Redirect(w, r, recBSrv.URL+"/redirected", http.StatusFound)
				return
			}
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

		// Direct cross-recipient credential send to B is refused before any TCP accept.
		respDirectB, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID:        "qual-cred-direct-b",
			CallID:             "call-qual-cred-direct-b",
			SessionID:          "sess-qual-cred",
			Actor:              "implement",
			View:               "workspace",
			Mode:               pythonruntime.ModeCode,
			Code:               `import requests, sys; requests.get(sys.argv[1], headers={"Authorization": "Bearer secret-for-a"})`,
			Args:               []string{recBSrv.URL + "/steal"},
			HTTP: portablepython.HTTPRequestOptions{
				Client:             recBSrv.Client(),
				CredentialScreener: scopedScreener,
			},
			Policy:             allowAll,
		})
		testutil.FailErr(t, "execute direct cross-recipient credential request to B", err)
		if respDirectB.CleanCompletion || recBGotAuth != "" || recBLn.accepts.Load() != 0 || recBHits.Load() != 0 {
			t.Fatalf("direct B leaked credential or connected: clean=%v auth=%q tcpAccepts=%d hits=%d", respDirectB.CleanCompletion, recBGotAuth, recBLn.accepts.Load(), recBHits.Load())
		}

		// Allowed redirect A -> B (both authorized): A receives Bearer token,
		// B ACTUALLY receives the redirected request (hits==1, tcpAccepts==1)
		// with ZERO Authorization header (stripped on cross-origin redirect).
		respRedirStrip, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID:        "qual-cred-redir-strip",
			CallID:             "call-qual-cred-redir-strip",
			SessionID:          "sess-qual-cred",
			Actor:              "implement",
			View:               "workspace",
			Mode:               pythonruntime.ModeCode,
			Code:               `import requests, sys; r = requests.get(sys.argv[1], headers={"Authorization": "Bearer secret-for-a"}); print(r.status_code, r.text)`,
			Args:               []string{recASrv.URL + "/redirect-to-b"},
			HTTP: portablepython.HTTPRequestOptions{
				Client:             combinedTLSClient(recASrv, recBSrv),
				CredentialScreener: scopedScreener,
			},
			Policy:             allowAll,
		})
		testutil.FailErr(t, "execute allowed cross-origin redirect A -> B", err)
		if !respRedirStrip.CleanCompletion || strings.TrimSpace(respRedirStrip.Stdout) != "200 redirected-b-ok" {
			t.Fatalf("allowed redirect A -> B failed: resp=%+v stop=%+v", respRedirStrip, respRedirStrip.Stop)
		}
		if recAGotAuth != "Bearer secret-for-a" {
			t.Fatalf("recipient A auth = %q, want %q", recAGotAuth, "Bearer secret-for-a")
		}
		if recBHits.Load() != 1 || recBLn.accepts.Load() != 1 {
			t.Fatalf("recipient B did not actually receive redirected request: hits=%d tcpAccepts=%d", recBHits.Load(), recBLn.accepts.Load())
		}
		if recBGotAuth != "" {
			t.Fatalf("recipient B unexpectedly received stripped Authorization header: %q", recBGotAuth)
		}

		// 1d. Partial streamed delivery interrupted mid-stream: assert guest
		// actually received the exact first chunk BEFORE cancellation and
		// compare byte counts with the reopened durable disposition.
		const firstChunk = "FIRST_CHUNK_64B_0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
		if len(firstChunk) != 64 {
			t.Fatalf("expected 64-byte test chunk, got %d", len(firstChunk))
		}
		guestAcked := make(chan struct{})
		releaseStream := make(chan struct{})
		streamSrv, _ := newCountingTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ack" {
				select {
				case <-guestAcked:
				default:
					close(guestAcked)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("acked"))
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte(firstChunk))
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-releaseStream:
			case <-r.Context().Done():
			}
		}))
		defer close(releaseStream)

		streamCtx, cancelStream := context.WithCancel(ctx)
		go func() {
			select {
			case <-guestAcked:
				time.Sleep(100 * time.Millisecond)
				cancelStream()
			case <-time.After(15 * time.Second):
				cancelStream()
			}
		}()
		respStream, err := svc.ExecuteTool(streamCtx, portablepython.ToolRequest{
			ExecutionID: "qual-partial-stream",
			CallID:      "call-qual-partial-stream",
			SessionID:   "sess-qual-stream",
			Actor:       "implement",
			View:        "workspace",
			Mode:        pythonruntime.ModeCode,
			Code: `import requests, sys
r = requests.get(sys.argv[1], stream=True)
chunk = next(r.iter_content(chunk_size=64))
print("CHUNK_RECEIVED:" + str(len(chunk)), flush=True)
requests.get(sys.argv[2])
next(r.iter_content(chunk_size=64))`,
			Args:       []string{streamSrv.URL + "/stream", streamSrv.URL + "/ack"},
			HTTP:       portablepython.HTTPRequestOptions{Client: streamSrv.Client()},
			Policy:     allowAll,
		})
		testutil.FailErr(t, "execute partial stream", err)
		if respStream.CleanCompletion {
			t.Fatalf("expected interrupted stream to not cleanly complete: %+v", respStream)
		}
		if !strings.Contains(respStream.Stdout, "CHUNK_RECEIVED:64") {
			t.Fatalf("guest stdout did not confirm first 64-byte chunk before cancellation: stdout=%q stderr=%q", respStream.Stdout, respStream.Stderr)
		}

		// Reopen through a fresh execution.Build runtime and verify durable
		// admission byte count parity and non-clean disposition.
		_ = svc.Close(ctx)
		rtStreamReopen, _ := buildQualificationRuntime(t, cfgDir)
		defer func() { _ = rtStreamReopen.Host.Commands.PortablePython().Close(ctx) }()

		adms, err := rtStreamReopen.Host.Commands.PythonPersistedAdmissions("qual-partial-stream")
		testutil.FailErr(t, "load persisted admissions through composed runtime", err)
		var streamAdm *pythonevidence.LedgerAdmission
		for i := range adms {
			if adms[i].Record.Family == pythonruntime.EffectHTTP && adms[i].Record.Target.RelPath == "/stream" {
				streamAdm = &adms[i]
				break
			}
		}
		if streamAdm == nil {
			t.Fatalf("reopened receipt store missing /stream HTTP admission: %+v", adms)
		}
		if streamAdm.Record.Outcome == nil {
			t.Fatalf("persisted /stream HTTP admission missing Outcome: %+v", streamAdm.Record)
		}
		if streamAdm.Record.Outcome.Succeeded {
			t.Fatalf("persisted /stream HTTP admission unexpectedly succeeded: %+v", streamAdm.Record.Outcome)
		}
		// Compare host-read bytes (Outcome.BytesTransferred) against the guest-delivered
		// lower bound confirmed on stdout (CHUNK_RECEIVED:64): host-read bytes must be
		// at least the guest-delivered chunk length.
		if streamAdm.Record.Outcome.BytesTransferred < int64(len(firstChunk)) {
			t.Fatalf("persisted /stream host-read BytesTransferred = %d, want >= %d (guest-delivered chunk lower bound)", streamAdm.Record.Outcome.BytesTransferred, len(firstChunk))
		}
		if streamAdm.Record.Outcome.Error == "" {
			t.Fatalf("persisted /stream HTTP admission missing cancellation error: %+v", streamAdm.Record.Outcome)
		}
		streamProj, err := rtStreamReopen.Host.Commands.PythonDenProjection("qual-partial-stream", pythonevidence.ViewerScope{
			Role:      pythonevidence.RoleCoordinator,
			SessionID: "sess-qual-stream",
			Actor:     "implement",
			View:      "workspace",
		})
		testutil.FailErr(t, "project partial stream after reopen", err)
		if streamProj.CleanCompletion {
			t.Fatalf("expected reopened partial-stream Den projection CleanCompletion=false, got %+v", streamProj)
		}
	})

	// 2. Durable persisted approvals across Approval (dedup), Decline, Cancel, and
	// Restart (reopen) exercised through the composed registered `python` tool
	// (`rt.Host.Registry.Run`), NOT direct `svc.ExecuteTool` with supplied policy.
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
		svc1.SetDefaultHTTPClient(srv.Client())
		wsDir := t.TempDir()

		waitForPendingLeaseByCall := func(b *pythonadmission.ApprovalBroker, callID string) pythonadmission.ApprovalLease {
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				for _, l := range b.Registry().List() {
					if l.CallID == callID && l.State == pythonadmission.LeasePending {
						return l
					}
				}
				time.Sleep(15 * time.Millisecond)
			}
			t.Fatalf("timed out waiting for pending lease for call %s", callID)
			return pythonadmission.ApprovalLease{}
		}
		holdTC := func(callID string) tools.ToolContext {
			tc := workspaceTC("sess-qual-hold", "implement", "", wsDir)
			tc.Identity.ToolCallID = callID
			return tc
		}

		// 2a. Approve once through rt1.Host.Registry.Run("python") (and repeat same approval -> error, still 1 body).
		var approvedLeaseID string
		approveDone := make(chan struct{})
		go func() {
			defer close(approveDone)
			l := waitForPendingLeaseByCall(broker1, "call-qual-hold-approve")
			approvedLeaseID = l.LeaseID
			_ = broker1.Approve(l.LeaseID, "approved once")
		}()
		respApp := runInstalledPython(t, ctx, rt1, map[string]any{
			"code": `import requests, sys; r = requests.post(sys.argv[1], data="once"); print(r.text)`,
			"args": []any{srv.URL + "/approve"},
		}, holdTC("call-qual-hold-approve"))
		<-approveDone
		if !respApp.CleanCompletion || strings.TrimSpace(respApp.Stdout) != "OK:once" || bodyCount.Load() != 1 {
			t.Fatalf("approve once mismatch: resp=%+v stop=%+v bodies=%d", respApp, respApp.Stop, bodyCount.Load())
		}
		if dupErr := broker1.Approve(approvedLeaseID, "duplicate approve"); dupErr == nil {
			t.Fatal("expected duplicate Approve on consumed lease to fail")
		}
		if bodyCount.Load() != 1 {
			t.Fatalf("duplicate approval caused extra body delivery: %d", bodyCount.Load())
		}

		// 2b. Decline through rt1.Host.Registry.Run("python") -> 0 additional bodies, 0 additional TCP accepts.
		acceptsBeforeDecline := srvLn.accepts.Load()
		declineDone := make(chan struct{})
		go func() {
			defer close(declineDone)
			l := waitForPendingLeaseByCall(broker1, "call-qual-hold-decline")
			_ = broker1.Decline(l.LeaseID, "declined")
		}()
		respDec := runInstalledPython(t, ctx, rt1, map[string]any{
			"code": `import requests, sys; requests.post(sys.argv[1], data="declined")`,
			"args": []any{srv.URL + "/decline"},
		}, holdTC("call-qual-hold-decline"))
		<-declineDone
		if respDec.CleanCompletion || respDec.Stop == nil || respDec.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("decline = %+v, want COMMAND_EFFECT_NOT_ALLOWED", respDec)
		}
		if bodyCount.Load() != 1 || srvLn.accepts.Load() != acceptsBeforeDecline {
			t.Fatalf("declined POST connected: bodies=%d accepts=%d (before=%d)", bodyCount.Load(), srvLn.accepts.Load(), acceptsBeforeDecline)
		}

		// 2c. Cancel / Stop while held through rt1.Host.Registry.Run("python") -> 0 additional bodies, 0 additional TCP accepts.
		acceptsBeforeCancel := srvLn.accepts.Load()
		cancelCtx, cancelHold := context.WithCancel(ctx)
		cancelDone := make(chan struct{})
		go func() {
			defer close(cancelDone)
			_ = waitForPendingLeaseByCall(broker1, "call-qual-hold-cancel")
			cancelHold()
		}()
		respCan := runInstalledPython(t, cancelCtx, rt1, map[string]any{
			"code": `import requests, sys; requests.post(sys.argv[1], data="cancelled")`,
			"args": []any{srv.URL + "/cancel"},
		}, holdTC("call-qual-hold-cancel"))
		<-cancelDone
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
			_, _ = rt1.Host.Registry.Run(restartCtx, "python", map[string]any{
				"code": `import requests, sys; requests.post(sys.argv[1], data="lost-on-restart")`,
				"args": []any{srv.URL + "/restart"},
			}, holdTC("call-qual-hold-restart"))
		}()
		lostLease := waitForPendingLeaseByCall(broker1, "call-qual-hold-restart")
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

	// 3. PY05 Files/Den/Session Reopen Binding & Cross-Session/Worker Isolation
	// exercised through a fresh composed execution.Build runtime (never opening
	// FileReceiptStore or BlobRetainedStreamPort manually in the test).
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

		// Close the first runtime completely and reopen via a fresh execution.Build
		// instance on the same cfgDir.
		_ = rt1.Host.Commands.PortablePython().Close(ctx)
		rt2, _ := buildQualificationRuntime(t, cfgDir)
		defer func() { _ = rt2.Host.Commands.PortablePython().Close(ctx) }()

		// Authorized coordinator scope sees persisted publications and reads disk-backed retained output.
		authScope := pythonevidence.ViewerScope{
			Role:      pythonevidence.RoleCoordinator,
			SessionID: "sess-reopen-a",
			Actor:     "implement",
			View:      string(workspaceTC("sess-reopen-a", "implement", "", wsDir).Source.SourceWorkspaceKind),
		}
		projWrite, err := rt2.Host.Commands.PythonDenProjection(writeResp.ExecutionID, authScope)
		testutil.FailErr(t, "project persisted write execution for Den via composed runtime", err)
		if !projWrite.Visible || len(projWrite.PublishedDiffs) == 0 {
			t.Fatalf("expected visible projection with published diffs after reopen, got %+v", projWrite)
		}
		if projWrite.PublishedDiffs[0].AttributionKind != pythonevidence.AttributionPythonWorkspacePublication {
			t.Fatalf("unexpected attribution kind after reopen: %+v", projWrite.PublishedDiffs[0])
		}

		projDeny, err := rt2.Host.Commands.PythonDenProjection(denyResp.ExecutionID, authScope)
		testutil.FailErr(t, "project persisted deny execution for Den via composed runtime", err)
		if !projDeny.Visible || len(projDeny.RefusedChildren) == 0 {
			t.Fatalf("expected visible projection with refused children after reopen, got %+v", projDeny)
		}

		retainedBytes, err := rt2.Host.Commands.ReadPythonRetainedOutput(ctx, authScope, writeResp.RetainedStdoutArtifactID)
		testutil.FailErr(t, "read persisted retained stdout bytes after reopen via composed runtime", err)
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
			projDenied, err := rt2.Host.Commands.PythonDenProjection(writeResp.ExecutionID, deniedScope)
			testutil.FailErr(t, "project for denied scope via composed runtime", err)
			if projDenied.Visible || !projDenied.RedactedForVisibility {
				t.Fatalf("denied scope %+v unexpectedly saw projection: %+v", deniedScope, projDenied)
			}
			if _, rErr := rt2.Host.Commands.ReadPythonRetainedOutput(ctx, deniedScope, writeResp.RetainedStdoutArtifactID); !errors.Is(rErr, pythonevidence.ErrAccessDenied) {
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

// TestPortablePythonCombinedCommandAndOAR exercises the common trusted
// invocation builder through the normal `command` tool entry:
//  1. Managed `python3` and `pytest` when direct `python` is NOT offered, with
//     a distinguishable decoy `python3` on PATH (proving Portable Python WASI
//     runs, not the host decoy) and shared profile authority (`worker_readonly`
//     on `implement_investigate` stops workspace writes).
//  2. Bare `python3` after a preceding completed shell effect (`echo prior_ok >
//     prior.txt && python3 -c ...`) when direct `python` IS offered -> refuses
//     with PYTHON_TOOL_REQUIRED while preserving `prior.txt`.
//  3. Explicit project runner (`./.venv/bin/python`) executes cleanly without
//     triggering PYTHON_TOOL_REQUIRED.
func TestPortablePythonCombinedCommandAndOAR(t *testing.T) {
	ctx := context.Background()
	rt, _ := buildInstalledExecutionRuntime(t)

	// Wire production command program services (Recorder, Blobs, Projector,
	// Progress, Sessions, Provenance, SourceObserver) onto rt.Host.Commands.
	fixture := programtest.New(t, programtest.Options{})
	origProg := fixture.Runtime
	testutil.FailErr(t, "compose production program services", rt.Host.Commands.SetProgramServices(toolhost.ProgramServices{
		Recorder:       fixture.Recorder,
		Blobs:          origProg.Blobs,
		Projector:      origProg.Projector,
		Progress:       origProg.Progress,
		Sessions:       origProg.Sessions,
		Provenance:     origProg.Provenance,
		SourceObserver: origProg.SourceObserver,
	}))
	rt.Host.Commands.SetBackgroundRegistry(fixture.Background)

	wsDir, err := filepath.EvalSymlinks(fixture.Dir)
	testutil.FailErr(t, "canonicalize workspace dir", err)
	scratchDir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "canonicalize scratch dir", err)

	// Plant a distinguishable decoy python3 on PATH to prove managed command
	// routing selects Portable Python WASI rather than the host PATH binary.
	decoyBinDir := t.TempDir()
	decoyPy := filepath.Join(decoyBinDir, "python3")
	testutil.FailErr(t, "write decoy python3", os.WriteFile(decoyPy, []byte("#!/bin/sh\necho HOST_DECOY_PYTHON\n"), 0o755))
	t.Setenv("PATH", decoyBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Plant an explicit project runner at ./.venv/bin/python.
	venvBinDir := filepath.Join(wsDir, ".venv", "bin")
	testutil.FailErr(t, "mkdir .venv/bin", os.MkdirAll(venvBinDir, 0o755))
	venvPy := filepath.Join(venvBinDir, "python")
	testutil.FailErr(t, "write .venv/bin/python", os.WriteFile(venvPy, []byte("#!/bin/sh\necho PROJECT_VENV_PYTHON_OK\n"), 0o755))

	runCmd := func(agent, surface string, offered []string, cmdSrc string) (string, error) {
		return fixture.Invoke(t, ctx, "command", map[string]any{"command": cmdSrc}, func(callCtx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
			tc.Identity.Agent = agent
			tc.Turn.TurnSurfaceID = surface
			tc.Turn.TurnOfferedToolNames = offered
			tc.Host.SessionScratchDir = scratchDir
			tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { return nil }
			return rt.Host.Registry.Run(callCtx, "command", args, tc)
		})
	}

	// 1a. Managed `python3` via `command` when direct `python` is NOT offered
	// runs Portable Python WASI (sys.platform == "wasi"), NOT the host decoy.
	outManaged, err := runCmd("implement", "", []string{"command"}, `python3 -c "import sys; print('PLATFORM:' + sys.platform)"`)
	testutil.FailErr(t, "run managed python3 via command tool", err)
	if !strings.Contains(outManaged, "PLATFORM:wasi") {
		t.Fatalf("expected Portable Python WASI output via command tool, got: %q", outManaged)
	}
	if strings.Contains(outManaged, "HOST_DECOY_PYTHON") {
		t.Fatalf("command tool unexpectedly executed host decoy python3 on PATH: %q", outManaged)
	}

	// 1a-ii. Permitted-by-profile (`implement`) but unoffered/unknown turn offer
	// (TurnOfferedToolNames == nil) preserves the established portable `command`
	// route and MUST NOT issue PYTHON_TOOL_REQUIRED from profile permission alone.
	outUnknownOffer, err := runCmd("implement", "", nil, `python3 -c "import sys; print('UNKNOWN_OFFER:' + sys.platform)"`)
	testutil.FailErr(t, "run managed python3 via command tool with unknown turn offer", err)
	if !strings.Contains(outUnknownOffer, "UNKNOWN_OFFER:wasi") {
		t.Fatalf("expected Portable Python WASI output when turn offer is unknown, got: %q", outUnknownOffer)
	}
	if strings.Contains(outUnknownOffer, "HOST_DECOY_PYTHON") || strings.Contains(outUnknownOffer, "PYTHON_TOOL_REQUIRED") {
		t.Fatalf("unknown turn offer must not fall back to host decoy or refuse with PYTHON_TOOL_REQUIRED: %q", outUnknownOffer)
	}

	// 1b. Shared authority: restricted worker (`worker_readonly`) on broader
	// surface (`implement_investigate`) via `command` stops workspace write.
	_, _ = runCmd("worker_readonly", "implement_investigate", []string{"command"}, `python3 -c "open('blocked_via_cmd.txt', 'w').write('nope')"`)
	if _, statErr := os.Stat(filepath.Join(wsDir, "blocked_via_cmd.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("blocked_via_cmd.txt must not exist under worker_readonly via command tool, statErr=%v", statErr)
	}

	// 1c. Managed `pytest` via `command` runs in Portable Python WASI.
	testutil.FailErr(t, "write test_cmd_smoke.py", os.WriteFile(
		filepath.Join(wsDir, "test_cmd_smoke.py"),
		[]byte("def test_cmd_ok():\n    assert 3 * 3 == 9\n"),
		0o644,
	))
	outPytest, err := runCmd("implement", "", []string{"command"}, "pytest -q")
	testutil.FailErr(t, "run managed pytest via command tool", err)
	if !strings.Contains(outPytest, "1 passed") {
		t.Fatalf("expected managed pytest via command tool to pass, got: %q", outPytest)
	}

	// 2. Preceding completed shell effect + bare `python3` when direct `python`
	// IS offered -> refuses with PYTHON_TOOL_REQUIRED while preserving prior.txt!
	outRefuse, refuseErr := runCmd("implement", "", []string{"command", "python"}, `echo prior_ok > prior.txt && python3 -c "print('SHOULD_NOT_RUN')"`)
	reject := toolrejection.AsToolReject(refuseErr)
	hasCodeInErr := reject != nil && reject.Code == "PYTHON_TOOL_REQUIRED"
	hasCodeInOut := strings.Contains(outRefuse, "PYTHON_TOOL_REQUIRED")
	if !hasCodeInErr && !hasCodeInOut {
		t.Fatalf("expected PYTHON_TOOL_REQUIRED refusal, got out=%q err=%v", outRefuse, refuseErr)
	}
	priorBytes, err := os.ReadFile(filepath.Join(wsDir, "prior.txt"))
	testutil.FailErr(t, "read prior.txt preserved after PYTHON_TOOL_REQUIRED", err)
	if strings.TrimSpace(string(priorBytes)) != "prior_ok" {
		t.Fatalf("prior.txt = %q, want %q", string(priorBytes), "prior_ok")
	}

	// 3. Explicit project runner (`./.venv/bin/python`) runs cleanly even when
	// direct `python` IS offered (never triggers PYTHON_TOOL_REQUIRED).
	outVenv, err := runCmd("implement", "", []string{"command", "python"}, `./.venv/bin/python -c "print('ignored')"`)
	testutil.FailErr(t, "run explicit .venv/bin/python via command tool", err)
	if !strings.Contains(outVenv, "PROJECT_VENV_PYTHON_OK") {
		t.Fatalf("expected explicit project runner output, got: %q", outVenv)
	}
}
