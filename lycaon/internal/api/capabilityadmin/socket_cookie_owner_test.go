//go:build linux || darwin

package capabilityadmin

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
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
	"github.com/lycaon/lycaon/internal/tools"
)

func TestUnixSocketCookieIssuerReplacementNeedsFreshDisclosureAuthority(t *testing.T) {
	for name, value := range map[string]string{"persistent": "local-cookie-value-4411", "short_session": "x"} {
		t.Run(name, func(t *testing.T) { testUnixSocketCookieIssuerReplacement(t, value) })
	}
}

func testUnixSocketCookieIssuerReplacement(t *testing.T, value string) {
	root, err := os.MkdirTemp("/tmp", "ck-") //nolint:usetesting // Native socket references have bounded addresses.
	testutil.FailErr(t, "cookie owner fixture", err)
	defer os.RemoveAll(root)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	workspace := filepath.Join(root, "workspace")
	testutil.FailErr(t, "cookie workspace", os.Mkdir(workspace, 0700))
	path := filepath.Join(root, "service")
	server, originalCookieRequests := startCookieIssuer(t, path, value)
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
	wire := func() *toolexecution.Executor {
		return newCookieOwnerExecutor(t, ctx, database, review, authority, runtime, service, matcher)
	}
	executor := wire()
	defer confine.SetEgressResolver(nil)
	args := map[string]any{"url": "http://127.0.0.1/private/info", "unix_socket": path, "cookie_jar": "service-cookies"}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "chat", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "issue", Agent: "implement"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: workspace, IsPrimary: true}}, ActiveRootID: "root"}}
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	testutil.FailErr(t, "capture service cookie", err)
	tc.Identity.ToolCallID = "same-issuer"
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	testutil.FailErr(t, "return cookie to live issuer", err)
	if originalCookieRequests.Load() != 1 || review.secretCards != 0 {
		t.Fatalf("same issuer: original-cookie-requests=%d, secret-cards=%d, cards=%d", originalCookieRequests.Load(), review.secretCards, review.cards)
	}
	jarRequest := secretcap.CookieJarRequest{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "chat", SessionID: "chat", OperationID: "inspect", Name: "service-cookies"}
	jar, err := service.OpenCookieJar(ctx, jarRequest)
	testutil.FailErr(t, "inspect original cookie", err)
	before := jar.Store.Snapshot()
	if len(before) != 1 || before[0].NativeIssuer == "" {
		t.Fatal("issuer receipt missing from protected cookie data")
	}
	testutil.FailErr(t, "stop cookie issuer", server.Close())
	decoy, err := net.Listen("unix", path)
	testutil.FailErr(t, "replacement cookie listener", err)
	observed := make(chan cookieObservation, 16)
	var reissue atomic.Bool
	var decoyRequests, decoyCookieBytes atomic.Int64
	decoyServer := &http.Server{Handler: cookieReplacementHandler(t, value, observed, &reissue, &decoyRequests, &decoyCookieBytes)}
	go func() { _ = decoyServer.Serve(decoy) }()
	defer decoyServer.Close()
	review.denySecrets = true
	beforeCards, beforeSecrets := review.cards, review.secretCards
	tc.Identity.ToolCallID = "new-socket-recipient"
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	if err != nil || review.cards-beforeCards != 2 || review.secretCards-beforeSecrets != 1 || decoyRequests.Load() != 1 || decoyCookieBytes.Load() != 0 {
		t.Fatalf("replacement cookie recipient: error=%v, cards=%d, secret_cards=%d, requests=%d, cookie_bytes=%d", err, review.cards-beforeCards, review.secretCards-beforeSecrets, decoyRequests.Load(), decoyCookieBytes.Load())
	}
	<-observed
	assertCookieDecisionLedger(t, ctx, authzcontext.NewSQLStore(database), "new-socket-recipient", authzcontext.EventOutcomeDenied)
	restored, err := service.OpenCookieJar(ctx, jarRequest)
	testutil.FailErr(t, "preserve cookie data", err)
	if !reflect.DeepEqual(restored.Store.Snapshot(), before) {
		t.Fatal("recipient refusal changed stored cookies")
	}
	invoke := func(call string, request map[string]any, deny bool, wantCookies bool) cookieObservation {
		return invokeCookieReplacement(t, ctx, executor, review, &tc, observed, authzcontext.NewSQLStore(database), call, request, deny, wantCookies)
	}
	testCookieReferenceDisclosure(t, value, args, invoke)
	invoke("approval-was-only-one-invocation", args, true, false)
	stillOriginal, err := service.OpenCookieJar(ctx, jarRequest)
	testutil.FailErr(t, "inspect invocation-only approval", err)
	if !reflect.DeepEqual(stillOriginal.Store.Snapshot(), before) {
		t.Fatal("approval or withholding rewrote issuer or attributes")
	}

	testCookieRedirectDisclosure(t, ctx, executor, review, &tc, args, observed, authzcontext.NewSQLStore(database))

	reissue.Store(true)
	invoke("actual-issuer-reissues", args, true, false)
	reissue.Store(false)
	reissued, err := service.OpenCookieJar(ctx, jarRequest)
	testutil.FailErr(t, "inspect actual reissuance", err)
	after := reissued.Store.Snapshot()
	if len(after) != 1 || after[0].NativeIssuer == "" || after[0].NativeIssuer == before[0].NativeIssuer {
		t.Fatal("actual response did not establish new issuer")
	}
	before[0].NativeIssuer = after[0].NativeIssuer
	if !reflect.DeepEqual(after, before) {
		t.Fatal("reissuance lost stored cookie attributes")
	}
	prior := review.secretCards
	tc.Identity.ToolCallID = "reissued-first-party-cookie"
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	testutil.FailErr(t, "same new issuer after reissuance", err)
	if got := <-observed; got.cookie != "session="+value || review.secretCards != prior {
		t.Fatal("actual new issuer did not gain first-party reuse")
	}

	// Reconstruct the actual store, runtime, secret service and HTTP consumer.
	// Protected cookie bytes survive; the old runtime's object authority does not.
	runtime.Close()
	approvals.Close()
	testutil.FailErr(t, "drain checkpoints before restart", checkpoints.StopExpiryTimers(context.Background()))
	approvals, err = settings.NewApprovalStoreAt(filepath.Join(root, "approvals.yaml"))
	testutil.FailErr(t, "reopen approvals", err)
	defer approvals.Close()
	authority = settings.NewRuleApprovalGate(approvals, settings.NoSources())
	runtime = approvalstate.NewSocketCapabilityRuntime()
	defer runtime.Close()
	service = secretcap.NewWithStore(database, values, nil)
	service.SetFingerprinter(fingerprinter)
	checkpoints = hitl.NewCheckpoints(hitl.NewSQLStore(database), &events.Publisher{Hub: events.NewMemoryHub()}, authzcontext.SQLRecorder(database))
	checkpoints.Authority.SetApprovalAuthorityInstaller(&Installation{Gate: authority, Sockets: runtime, authorityMu: &sync.Mutex{}})
	review = &socketOwnerReview{Checkpoints: checkpoints, decisionContext: ctx}
	executor = wire()
	persisted, err := service.OpenCookieJar(ctx, jarRequest)
	testutil.FailErr(t, "reopen protected cookie after engine restart", err)
	if !reflect.DeepEqual(persisted.Store.Snapshot(), after) {
		t.Fatal("restart changed protected cookie receipt or attributes")
	}
	invoke("engine-restart-stale-cookie", args, true, false)
	if review.cards != 2 {
		t.Fatal("restart bypassed fresh socket and disclosure reviews")
	}

	testLegacyCookieIssuer(t, ctx, service, jarRequest, args, invoke, value)

	testOrdinaryCookieTransport(t, ctx, executor, review, &tc, args, invoke, decoyServer.Handler, observed, &reissue, value)
}

// startCookieIssuer owns the original endpoint and counts cookies returned to it.
func startCookieIssuer(t *testing.T, path, value string) (*http.Server, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "cookie issuer listener", err)
	requests := &atomic.Int64{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if cookie, err := req.Cookie("session"); err == nil && cookie.Value == value {
			requests.Add(1)
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: value, Path: "/private", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return server, requests
}

// The journal records the authenticated human disclosure, not a socket lease
// standing in for secret authority.
func assertCookieDecisionLedger(t *testing.T, ctx context.Context, store *authzcontext.SQLStore, call string, outcome authzcontext.EventOutcome) {
	t.Helper()
	rows, err := store.ListEvents(ctx, "chat")
	testutil.FailErr(t, "read real authorization journal", err)
	for _, row := range rows {
		var detail authzcontext.EventDetail
		testutil.FailErr(t, "decode authorization event", json.Unmarshal([]byte(row.DetailJSON), &detail))
		if detail.ToolCallID == call && row.Action == authzcontext.EventActionApprovalDecision && detail.Gate == string(api.GateSecretOutbound) {
			if row.Outcome != outcome || row.ResolvedBy != authzcontext.ResolvedByHuman || row.ResolverPersonID == "" {
				t.Fatalf("cookie disclosure authority = %+v", row)
			}
			return
		}
	}
	t.Fatalf("%s has no human secret-disclosure journal outcome", call)
}

type cookieObservation struct{ path, cookie, csrf, body string }
type cookieReplacementInvoke func(string, map[string]any, bool, bool) cookieObservation

func newCookieOwnerExecutor(t *testing.T, ctx context.Context, database db.Handle, review *socketOwnerReview, authority hitl.ApprovalGate, runtime *approvalstate.SocketCapabilityRuntime, service *secretcap.Service, matcher *secretmatch.Matcher) *toolexecution.Executor {
	t.Helper()
	registry := tools.NewDefaultRegistry()
	executor := toolexecution.NewExecutor(nil, registry, "implement")
	executor.Approvals.SetCheckpointManager(ctx, review, authority)
	executor.Approvals.SetAuthzRecorder(authzcontext.SQLRecorder(database))
	executor.Network.SetEgressPostureSource(func(string) gate.Posture { return gate.PostureStrict })
	executor.Capabilities.SetSocketCapabilityRuntime(runtime)
	executor.Secrets.SetSecretResolver(service)
	executor.Secrets.SetSecretMatcher(matcher)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{"http_request": true}}})
	testutil.FailErr(t, "register cookie native consumer", httpaction.Register(registry, httpaction.Deps{Boundary: boundary, Secrets: service, SecretMatcher: matcher, SecretAsk: executor.Secrets.AskSecretScreen}))
	return executor
}

func invokeCookieReplacement(t *testing.T, ctx context.Context, executor *toolexecution.Executor, review *socketOwnerReview, tc *tools.ToolContext, observed <-chan cookieObservation, journal *authzcontext.SQLStore, call string, request map[string]any, deny, wantCookies bool) cookieObservation {
	t.Helper()
	review.denySecrets = deny
	prior := review.secretCards
	tc.Identity.ToolCallID = call
	_, err := executor.Invoke(ctx, "http_request", request, *tc)
	testutil.FailErr(t, call, err)
	if review.secretCards-prior != 1 {
		t.Fatalf("%s: disclosure cards=%d, want one", call, review.secretCards-prior)
	}
	select {
	case got := <-observed:
		if (got.cookie != "") != wantCookies {
			t.Fatalf("%s: cookie bytes=%d, want release=%v", call, len(got.cookie), wantCookies)
		}
		outcome := authzcontext.EventOutcomeAllowed
		if deny {
			outcome = authzcontext.EventOutcomeDenied
		}
		assertCookieDecisionLedger(t, ctx, journal, call, outcome)
		return got
	default:
		t.Fatalf("%s: request did not proceed", call)
		return cookieObservation{}
	}
}

func testLegacyCookieIssuer(t *testing.T, ctx context.Context, service *secretcap.Service, jarRequest secretcap.CookieJarRequest, args map[string]any, invoke cookieReplacementInvoke, value string) {
	t.Helper()
	legacyRequest := jarRequest
	legacyRequest.Name, legacyRequest.OperationID = "legacy-cookies", "legacy-seed"
	legacy, err := service.OpenCookieJar(ctx, legacyRequest)
	testutil.FailErr(t, "open pre-change jar", err)
	legacyURL, err := url.Parse(args["url"].(string))
	testutil.FailErr(t, "legacy cookie target", err)
	legacy.Store.SetCookies(legacyURL, []*http.Cookie{{Name: "session", Value: value, Path: "/private", HttpOnly: true}})
	_, err = service.SaveCookieJar(ctx, legacyRequest, legacy)
	testutil.FailErr(t, "persist pre-change protected value", err)
	// Compare persisted forms: the seeded store's clock readings carry state
	// that protected storage does not.
	seeded, err := service.OpenCookieJar(ctx, legacyRequest)
	testutil.FailErr(t, "reopen pre-change jar", err)
	legacyBefore := seeded.Store.Snapshot()
	if len(legacyBefore) != 1 || legacyBefore[0].NativeIssuer != "" {
		t.Fatal("legacy fixture has an issuer")
	}
	legacyArgs := maps.Clone(args)
	legacyArgs["cookie_jar"] = legacyRequest.Name
	invoke("legacy-cookie-unknown-issuer", legacyArgs, true, false)
	reopenedLegacy, err := service.OpenCookieJar(ctx, legacyRequest)
	testutil.FailErr(t, "preserve old entry", err)
	if !reflect.DeepEqual(reopenedLegacy.Store.Snapshot(), legacyBefore) {
		t.Fatal("legacy load or denial rewrote saved data")
	}
}

func testOrdinaryCookieTransport(t *testing.T, ctx context.Context, executor *toolexecution.Executor, review *socketOwnerReview, tc *tools.ToolContext, args map[string]any, invoke cookieReplacementInvoke, handler http.Handler, observed <-chan cookieObservation, reissue *atomic.Bool, value string) {
	t.Helper()
	ordinaryServer := httptest.NewServer(handler)
	defer ordinaryServer.Close()
	ordinaryURL, err := url.Parse(ordinaryServer.URL)
	testutil.FailErr(t, "ordinary service URL", err)
	port, err := strconv.Atoi(ordinaryURL.Port())
	testutil.FailErr(t, "ordinary service port", err)
	localPort, err := strconv.ParseUint(ordinaryURL.Port(), 10, 16)
	testutil.FailErr(t, "ordinary service port range", err)
	ordinaryArgs := maps.Clone(args)
	delete(ordinaryArgs, "unix_socket")
	ordinaryArgs["url"] = ordinaryServer.URL + "/private/info"
	ordinaryArgs["capability_request"] = map[string]any{"loopback_connect": map[string]any{"ports": []any{port}}}
	// Standing chat authority for the ordinary port keeps this leg about cookies.
	tc.Local.LoopbackConnectGranted, tc.Local.LoopbackConnectPorts = true, []uint16{uint16(localPort)}
	invoke("native-cookie-to-ordinary-transport", ordinaryArgs, true, false)
	ordinaryArgs["cookie_jar"] = "ordinary-cookies"
	reissue.Store(true)
	tc.Identity.ToolCallID = "ordinary-cookie-issuance"
	prior := review.secretCards
	_, err = executor.Invoke(ctx, "http_request", ordinaryArgs, *tc)
	testutil.FailErr(t, "ordinary response issues cookie", err)
	if got := <-observed; got.cookie != "" || review.secretCards != prior {
		t.Fatal("empty ordinary jar required disclosure")
	}
	reissue.Store(false)
	tc.Identity.ToolCallID = "ordinary-first-party-reuse"
	_, err = executor.Invoke(ctx, "http_request", ordinaryArgs, *tc)
	testutil.FailErr(t, "ordinary first-party request", err)
	if got := <-observed; got.cookie != "session="+value || review.secretCards != prior {
		t.Fatal("ordinary URL cookie behavior changed")
	}
}

func cookieReplacementHandler(t *testing.T, value string, observed chan<- cookieObservation, reissue *atomic.Bool, decoyRequests, decoyCookieBytes *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			t.Errorf("read request: %v", readErr)
		}
		observed <- cookieObservation{path: req.URL.Path, cookie: req.Header.Get("Cookie"), csrf: req.Header.Get("X-CSRF"), body: string(body)}
		decoyRequests.Add(1)
		decoyCookieBytes.Add(int64(len(req.Header.Get("Cookie"))))
		if req.URL.Path == "/jump" {
			http.Redirect(w, req, "/private/info", http.StatusFound)
			return
		}
		if reissue.Load() {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: value, Path: "/private", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func testCookieRedirectDisclosure(t *testing.T, ctx context.Context, executor *toolexecution.Executor, review *socketOwnerReview, tc *tools.ToolContext, args map[string]any, observed <-chan cookieObservation, journal *authzcontext.SQLStore) {
	t.Helper()
	jump := maps.Clone(args)
	jump["url"], jump["redirects"] = "http://127.0.0.1/jump", "safe"
	review.denySecrets = true
	prior := review.secretCards
	tc.Identity.ToolCallID = "redirect-newly-eligible-cookie"
	_, err := executor.Invoke(ctx, "http_request", jump, *tc)
	testutil.FailErr(t, "redirect request proceeds after disclosure denial", err)
	firstHop, secondHop := <-observed, <-observed
	if firstHop.path != "/jump" || secondHop.path != "/private/info" || firstHop.cookie != "" || secondHop.cookie != "" || review.secretCards-prior != 1 {
		t.Fatalf("redirect cookie review: %+v %+v cards=%d", firstHop, secondHop, review.secretCards-prior)
	}
	assertCookieDecisionLedger(t, ctx, journal, tc.Identity.ToolCallID, authzcontext.EventOutcomeDenied)
}

func testCookieReferenceDisclosure(t *testing.T, value string, args map[string]any, invoke cookieReplacementInvoke) {
	t.Helper()
	referenced := maps.Clone(args)
	referenced["method"] = "POST"
	referenced["headers"] = []any{map[string]any{"name": "X-CSRF", "value": "prefix-{{cookie:session}}-suffix"}}
	referenced["body_text"] = "csrf={{cookie:session}}&field=1"
	deniedReference := invoke("cookie-reference-denied", referenced, true, false)
	if deniedReference.csrf != "prefix--suffix" || deniedReference.body != "csrf=&field=1" {
		t.Fatalf("withheld reference changed ordinary request fields: %+v", deniedReference)
	}
	approvedReference := invoke("cookie-reference-approved", referenced, false, true)
	if approvedReference.csrf != "prefix-"+value+"-suffix" || approvedReference.body != "csrf="+value+"&field=1" {
		t.Fatal("approved reference was not placed")
	}
}
