package execution_test

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

	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/tools"
)

// TestAppExecutionBuildAutoDiscoversPortablePythonImage verifies:
//  1. With PW_PORTABLE_PYTHON_IMAGE_DIR UNSET, staging the verified image into
//     configlayout.EngineRoot()/portable-python/image (the exact path
//     scripts/stage-engine.sh populates) lets execution.Build discover and
//     initialize portablepython.Service automatically.
//  2. The composed Service uses Painted Wolf's real *pythonevidence.HostSecretScreener
//     (secretmatch.Matcher) and disk-backed blobstore RetainedArtifactStore.
//  3. Installed-session HTTP policy (confine.DecideAttributedHTTPRequest) permits
//     an authorized outbound POST and refuses a denied outbound POST with 0 hits.
func TestAppExecutionBuildAutoDiscoversPortablePythonImage(t *testing.T) {
	sourceImageDir := strings.TrimSpace(os.Getenv(portablepython.EnvImageDir))
	if sourceImageDir == "" {
		t.Skip("PW_PORTABLE_PYTHON_IMAGE_DIR not set; skipping app composition root auto-discovery test")
	}

	// Stage the verified image into an isolated LYCAON_ENGINE_ROOT/portable-python/image
	// and explicitly UNSET PW_PORTABLE_PYTHON_IMAGE_DIR so discovery MUST use
	// the installed engine-root path.
	cfgDir := t.TempDir()
	engineRoot := t.TempDir()
	stagedParent := filepath.Join(engineRoot, "portable-python")
	testutil.FailErr(t, "mkdir staged engine-root parent", os.MkdirAll(stagedParent, 0o755))
	stagedImage := filepath.Join(stagedParent, "image")
	testutil.FailErr(t, "symlink verified image into engine-root", os.Symlink(sourceImageDir, stagedImage))

	t.Setenv(configdir.EnvConfigDir, cfgDir)
	t.Setenv(configlayout.EnvEngineRoot, engineRoot)
	t.Setenv(portablepython.EnvImageDir, "")

	discovered, err := portablepython.DiscoverImageDir()
	testutil.FailErr(t, "discover staged image from engine-root without env var", err)
	if discovered == "" {
		t.Fatal("DiscoverImageDir returned empty path")
	}

	approvalStore, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "create approval store", err)
	testutil.FailErr(t, "configure strict egress posture", approvalStore.PutGlobal(settings.ApprovalConfig{
		Posture: gate.PostureStrict,
	}))
	t.Cleanup(func() {
		confine.SetEgressRuleEvaluator(nil)
		confine.SetEgressPostureResolver(nil)
		confine.SetApprovalsDisabledSource(nil)
	})

	catalog := extpackstest.StockCatalog(t)
	rt, err := execution.Build(&settings.Service{Approvals: approvalStore}, "", catalog, decide.Reranker{}, nil)
	testutil.FailErr(t, "execution.Build with auto-discovered Portable Python", err)

	svc := rt.Host.Commands.PortablePython()
	if svc == nil {
		t.Fatal("expected execution.Build to auto-wire portablepython.Service")
	}

	// Confirm real HostSecretScreener (secretmatch.Matcher), not TestSecretScreener.
	if _, ok := svc.SecretScreener().(*pythonevidence.HostSecretScreener); !ok {
		t.Fatalf("expected *pythonevidence.HostSecretScreener in app composition, got %T", svc.SecretScreener())
	}

	// 1. Basic print(1) through the normal registered session tool.
	raw, err := rt.Host.Registry.Run(context.Background(), "python", map[string]any{
		"code": "print(1)",
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "sess-installed-1",
			Agent:      "implement",
			ToolCallID: "call-installed-1",
		},
	})
	testutil.FailErr(t, "execute registered python tool from app execution runtime", err)

	var resp portablepython.ToolResponse
	testutil.FailErr(t, "decode python tool response", json.Unmarshal([]byte(raw), &resp))
	if !resp.CleanCompletion || resp.ExitCode != 0 || strings.TrimSpace(resp.Stdout) != "1" {
		t.Fatalf("unexpected python tool response from app execution runtime: %+v", resp)
	}
	if resp.EvidenceSummary == nil || !resp.EvidenceSummary.Visible {
		t.Fatalf("expected non-nil visible EvidenceSummary, got %+v", resp.EvidenceSummary)
	}

	// 2. Verify overflow stdout is persisted via the real disk-backed blobstore RetainedStreamPort.
	bigRaw, err := rt.Host.Registry.Run(context.Background(), "python", map[string]any{
		"code": `print("B" * 80000)`,
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "sess-installed-1",
			Agent:      "implement",
			ToolCallID: "call-installed-big",
		},
	})
	testutil.FailErr(t, "execute big output python tool from app execution runtime", err)
	var bigResp portablepython.ToolResponse
	testutil.FailErr(t, "decode big output response", json.Unmarshal([]byte(bigRaw), &bigResp))
	if !bigResp.StdoutTruncated || bigResp.RetainedStdoutArtifactID == "" {
		t.Fatalf("expected truncated stdout with retained artifact ID, got %+v", bigResp)
	}
	art, err := svc.RetainedStore().Get(context.Background(), pythonevidence.ViewerScope{
		Role:      pythonevidence.RoleCoordinator,
		SessionID: "sess-installed-1",
		Actor:     "implement",
		View:      "workspace",
	}, bigResp.RetainedStdoutArtifactID)
	testutil.FailErr(t, "load disk-backed retained stdout artifact", err)
	if art.StreamRef == nil || art.StreamRef.Blob.ID == "" {
		t.Fatalf("expected real disk-backed StreamRef with Blob.ID, got %+v", art.StreamRef)
	}
	if len(art.Data) < 80000 {
		t.Fatalf("retained blob bytes = %d, want >= 80000", len(art.Data))
	}

	// 3. Prove BOTH a permitted outbound POST AND a refused outbound POST from
	// the installed session through confine.DecideAttributedHTTPRequest.
	var gotPostBody string
	var allowedHits atomic.Int32
	allowedSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowedHits.Add(1)
		body, _ := io.ReadAll(r.Body)
		gotPostBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"posted":true}`))
	}))
	defer allowedSrv.Close()
	allowedURL, err := url.Parse(allowedSrv.URL)
	testutil.FailErr(t, "parse allowed TLS server URL", err)

	var deniedHits atomic.Int32
	deniedSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deniedHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer deniedSrv.Close()
	deniedURL, err := url.Parse(deniedSrv.URL)
	testutil.FailErr(t, "parse denied TLS server URL", err)

	// Trust the test TLS server's CA on the service's HTTP client while keeping
	// production TLS enforcement and session egress policy active.
	svc.SetDefaultHTTPClient(allowedSrv.Client())

	var resolverCalls atomic.Int32
	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		resolverCalls.Add(1)
		if ep.RequestPath == "/blocked" {
			return false
		}
		return ep.RequestPath == "/submit" && ep.RequestMethod == http.MethodPost &&
			(ep.Host == allowedURL.Hostname() || ep.Host == allowedURL.Host)
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	_ = deniedURL

	// 3a. Permitted outbound POST succeeds and delivers JSON body.
	postRaw, err := rt.Host.Registry.Run(context.Background(), "python", map[string]any{
		"code": `import requests, sys; r = requests.post(sys.argv[1], json={"msg": "hello-installed-session"}); print(r.status_code, r.text)`,
		"args": []any{allowedSrv.URL + "/submit"},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "sess-installed-http",
			Agent:      "implement",
			ToolCallID: "call-installed-post-ok",
		},
	})
	testutil.FailErr(t, "execute permitted outbound POST from installed session", err)
	var postResp portablepython.ToolResponse
	testutil.FailErr(t, "decode permitted POST response", json.Unmarshal([]byte(postRaw), &postResp))
	if !postResp.CleanCompletion || postResp.ExitCode != 0 {
		t.Fatalf("permitted outbound POST failed: %+v", postResp)
	}
	if !strings.Contains(postResp.Stdout, "200") || !strings.Contains(postResp.Stdout, `"posted":true`) {
		t.Fatalf("unexpected POST stdout: %q", postResp.Stdout)
	}
	if allowedHits.Load() != 1 || !strings.Contains(gotPostBody, "hello-installed-session") {
		t.Fatalf("allowed server observed hits=%d body=%q", allowedHits.Load(), gotPostBody)
	}

	// 3b. Refused outbound POST stops with COMMAND_EFFECT_NOT_ALLOWED and 0 hits.
	denyRaw, err := rt.Host.Registry.Run(context.Background(), "python", map[string]any{
		"code": `import requests, sys; requests.post(sys.argv[1], json={"msg": "blocked"})`,
		"args": []any{deniedSrv.URL + "/blocked"},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "sess-installed-http",
			Agent:      "implement",
			ToolCallID: "call-installed-post-deny",
		},
	})
	testutil.FailErr(t, "execute refused outbound POST from installed session", err)
	var denyResp portablepython.ToolResponse
	testutil.FailErr(t, "decode refused POST response", json.Unmarshal([]byte(denyRaw), &denyResp))
	if denyResp.CleanCompletion || denyResp.Stop == nil || denyResp.Stop.Code != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("refused outbound POST = %+v, want COMMAND_EFFECT_NOT_ALLOWED", denyResp)
	}
	if deniedHits.Load() != 0 {
		t.Fatalf("denied server received %d hits, want 0", deniedHits.Load())
	}
}
