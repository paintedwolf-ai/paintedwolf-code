//go:build linux || darwin

package capabilityadmin

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestUnixSocketCookieIssuerReplacementNeedsFreshDisclosureAuthority(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "ck-") //nolint:usetesting // Native socket references have bounded addresses.
	testutil.FailErr(t, "cookie owner fixture", err)
	defer os.RemoveAll(root)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	workspace := filepath.Join(root, "workspace")
	testutil.FailErr(t, "cookie workspace", os.Mkdir(workspace, 0700))
	path := filepath.Join(root, "service")
	original, err := net.Listen("unix", path)
	testutil.FailErr(t, "cookie issuer listener", err)
	var originalCookieRequests atomic.Int64
	const value = "local-cookie-value-4411"
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if cookie, err := req.Cookie("session"); err == nil && cookie.Value == value {
			originalCookieRequests.Add(1)
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: value, Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(original) }()
	defer server.Close()
	database := testdbfixture.Open(t, "cookie-owner.db")
	testdbseed.InsertSessionWithRoot(t, database, "chat", testdbseed.DefaultProjectID, workspace)
	ctx := testdbseed.OwnerCaller(t, t.Context(), database)
	values := credentialstore.NewEmpty(credentialstore.Slot{Path: filepath.Join(root, credentialstore.VaultBasename), Namespace: credentialstore.NamespaceManagedSecrets, Context: "cookie owner fixture"}, func(string) bool { return true })
	service := secretcap.NewWithStore(database, values, nil)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "cookie matcher", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("h", 32)))
	testutil.FailErr(t, "cookie fingerprinter", err)
	matcher.SetFingerprinter(fingerprinter)
	service.SetFingerprinter(fingerprinter)
	approvals, err := settings.NewApprovalStoreAt(filepath.Join(root, "approvals.yaml"))
	testutil.FailErr(t, "cookie approvals", err)
	defer approvals.Close()
	authority := settings.NewRuleApprovalGate(approvals, settings.NoSources())
	runtime := approvalstate.NewSocketCapabilityRuntime()
	defer runtime.Close()
	checkpoints := hitl.NewCheckpoints(hitl.NewSQLStore(database), &events.Publisher{Hub: events.NewMemoryHub()}, authzcontext.SQLRecorder(database))
	defer func() {
		testutil.FailErr(t, "stop cookie checkpoints", checkpoints.StopExpiryTimers(context.Background()))
	}()
	checkpoints.Authority.SetApprovalAuthorityInstaller(&Installation{Gate: authority, Sockets: runtime, authorityMu: &sync.Mutex{}})
	review := &socketOwnerReview{Checkpoints: checkpoints, decisionContext: ctx}
	registry := tools.NewDefaultRegistry()
	executor := toolexecution.NewExecutor(nil, registry, "implement")
	executor.Approvals.SetCheckpointManager(ctx, review, authority)
	executor.Approvals.SetAuthzRecorder(authzcontext.SQLRecorder(database))
	defer confine.SetEgressResolver(nil)
	executor.Network.SetEgressPostureSource(func(string) gate.Posture { return gate.PostureStrict })
	executor.Capabilities.SetSocketCapabilityRuntime(runtime)
	executor.Secrets.SetSecretResolver(service)
	executor.Secrets.SetSecretMatcher(matcher)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{"http_request": true}}})
	testutil.FailErr(t, "register cookie native consumer", httpaction.Register(registry, httpaction.Deps{Boundary: boundary, Secrets: service, SecretMatcher: matcher, SecretAsk: executor.Secrets.AskSecretScreen}))
	args := map[string]any{"url": "http://service.invalid/info", "unix_socket": path, "cookie_jar": "service-cookies"}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "chat", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "issue", Agent: "implement"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: workspace, IsPrimary: true}}, ActiveRootID: "root"}}
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	testutil.FailErr(t, "capture service cookie", err)
	tc.Identity.ToolCallID = "same-issuer"
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	testutil.FailErr(t, "return cookie to live issuer", err)
	if originalCookieRequests.Load() != 1 || review.secretCards != 0 {
		t.Fatal("same issuer did not retain ordinary first-party cookie behavior")
	}
	jarRequest := secretcap.CookieJarRequest{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "chat", SessionID: "chat", OperationID: "inspect", Name: "service-cookies"}
	jar, err := service.OpenCookieJar(ctx, jarRequest)
	testutil.FailErr(t, "inspect original cookie", err)
	before := jar.Store.Values()
	testutil.FailErr(t, "stop cookie issuer", server.Close())
	decoy, err := net.Listen("unix", path)
	testutil.FailErr(t, "replacement cookie listener", err)
	var decoyRequests, decoyCookieBytes atomic.Int64
	decoyServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		decoyRequests.Add(1)
		decoyCookieBytes.Add(int64(len(req.Header.Get("Cookie"))))
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = decoyServer.Serve(decoy) }()
	defer decoyServer.Close()
	review.denySecrets = true
	beforeCards, beforeSecrets := review.cards, review.secretCards
	tc.Identity.ToolCallID = "new-socket-recipient"
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	if toolrejection.AsToolReject(err) == nil || review.secretCards <= beforeSecrets || decoyRequests.Load() != 0 || decoyCookieBytes.Load() != 0 {
		t.Fatalf("replacement cookie recipient: error=%v, cards=%d, secret_cards=%d, requests=%d, cookie_bytes=%d", err, review.cards-beforeCards, review.secretCards-beforeSecrets, decoyRequests.Load(), decoyCookieBytes.Load())
	}
	restored, err := service.OpenCookieJar(ctx, jarRequest)
	testutil.FailErr(t, "preserve cookie data", err)
	if !reflect.DeepEqual(restored.Store.Values(), before) {
		t.Fatal("recipient refusal changed stored cookies")
	}
}
