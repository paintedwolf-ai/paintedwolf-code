package httpaction

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// A cookie whose receipt names a retained service object is foreign to every
// other recipient. When its disclosure cannot be decided, the request must stop
// before any byte leaves and the protected jar must stay as it was.
// secretShapedCookie is a cookie value the bundled patterns recognize, so the
// outbound screen decides its disclosure. It is assembled so no token-shaped
// literal appears in source.
const secretShapedCookie = "gh" + "p_" + "wWPw5k4aXcaT4fNP" + "0UcnZwJUVFk6LO0pINUx"

func TestCookieScreenFaultsStopTheRequestBeforeDelivery(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "cookie matcher", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("f", 32)))
	testutil.FailErr(t, "cookie fingerprinter", err)
	matcher.SetFingerprinter(fingerprinter)

	cases := []struct {
		name    string
		matcher *secretmatch.Matcher
		ask     func(*atomic.Int64) secretmatch.AskFunc
		asked   int64
		stage   string
	}{
		{name: "disclosure_unwired", matcher: matcher, stage: secretmatch.FaultStageScreenUnwired},
		{name: "matcher_unavailable", ask: answering(nil), stage: secretmatch.FaultStageScreenUnwired},
		{name: "disclosure_error", matcher: matcher, ask: answering(errors.New("card store offline")), asked: 1, stage: secretmatch.FaultStageRaise},
		{name: "disclosure_fault", matcher: matcher, ask: answering(secretmatch.NewAskFault(secretmatch.FaultStageNoSession, nil)), asked: 1, stage: secretmatch.FaultStageNoSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secrets, _ := testSecrets(t)
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(server.Close)
			target, err := url.Parse(server.URL + "/private")
			testutil.FailErr(t, "cookie target", err)
			store := httpcookies.New(nil)
			store.SetCookies(target, []*http.Cookie{{Name: "session", Value: secretShapedCookie, Path: "/"}})
			before := store.Snapshot()

			var asked atomic.Int64
			deps := Deps{Boundary: testBoundary(), Secrets: secrets, SecretMatcher: tc.matcher}
			if tc.ask != nil {
				deps.SecretAsk = tc.ask(&asked)
			}
			_, err = runRequestWith(t, deps, map[string]any{
				"url": server.URL + "/private", "capability_request": loopbackCapability(t, server.URL),
			}, sessionContext(t.TempDir(), "ordinary-transport"), ExchangeOptions{Jar: testFrontendJar{store}})
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != toolrejection.OutboundSecretScreenFailedCode || reject.Data["fault_stage"] != tc.stage {
				t.Fatalf("error = %v, want %s at %s", err, toolrejection.OutboundSecretScreenFailedCode, tc.stage)
			}
			if requests.Load() != 0 || asked.Load() != tc.asked {
				t.Fatalf("requests=%d asks=%d, want no delivery and %d asks", requests.Load(), asked.Load(), tc.asked)
			}
			if !reflect.DeepEqual(store.Snapshot(), before) {
				t.Fatal("a disclosure fault changed the jar")
			}
		})
	}
}

func answering(err error) func(*atomic.Int64) secretmatch.AskFunc {
	return func(asked *atomic.Int64) secretmatch.AskFunc {
		return func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			asked.Add(1)
			return secretmatch.Resolution{}, err
		}
	}
}

func TestFrontendJarAndGrantFreshnessAcrossReviewWait(t *testing.T) {
	t.Run("unbacked_frontend_cookie_jar_fails_closed_before_delivery", testUnbackedFrontendCookieJarFailsClosedBeforeDelivery)

	t.Run("standing_secret_grant_revoked_across_wait_and_redirect_with_real_approval_store", testStandingGrantRevokedAcrossWaitAndRedirect)
}

func testUnbackedFrontendCookieJarFailsClosedBeforeDelivery(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	deps := Deps{
		Boundary:      testBoundary(),
		SecretMatcher: testSecretMatcher(t),
	}
	_, err := Perform(t.Context(), deps, map[string]any{
		"url":                server.URL + "/api",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(t.TempDir(), "unbacked-jar"), ExchangeOptions{
		Jar: unbackedCookieJar{},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != toolrejection.OutboundSecretScreenFailedCode {
		t.Fatalf("Perform with unbacked Jar err = %v, want %s", err, toolrejection.OutboundSecretScreenFailedCode)
	}
	if requests.Load() != 0 {
		t.Fatalf("server received %d requests with unbacked Jar, want 0", requests.Load())
	}
}

func testStandingGrantRevokedAcrossWaitAndRedirect(t *testing.T) {
	const cookieSecret = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
	var originHits, targetHits atomic.Int64
	var originCookie, targetCookie atomic.Value
	originCookie.Store("")
	targetCookie.Store("")

	var revokeTargetOnRedirect atomic.Bool
	var targetGrantID string
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approval store", err)
	authority := settings.NewRuleApprovalGate(store, settings.NoSources())
	review := &cookieGrantReview{t: t, authority: authority}

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		targetCookie.Store(r.Header.Get("Cookie"))
		_, _ = w.Write([]byte("target-ok"))
	}))
	t.Cleanup(targetServer.Close)

	originServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		originCookie.Store(r.Header.Get("Cookie"))
		if r.URL.Path == "/redirect" {
			if revokeTargetOnRedirect.Load() && targetGrantID != "" {
				removed, revokeErr := authority.RevokeGrant(targetGrantID)
				testutil.FailErr(t, "revoke target standing grant during Hop 0 redirect", revokeErr)
				if !removed {
					t.Error("expected target standing grant to be removed during Hop 0")
				}
			}
			http.Redirect(w, r, targetServer.URL+"/land", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("origin-ok"))
	}))
	t.Cleanup(originServer.Close)

	registry := tools.NewDefaultRegistry()
	executor := toolexecution.NewExecutor(nil, registry, "implement")
	executor.Approvals.SetCheckpointManager(t.Context(), review, authority)
	t.Cleanup(func() { restoreResolver() })
	// A chat that has read nothing external: its first contacts are audited,
	// so the cards here are the cookie disclosures alone.
	executor.Secrets.SetUntrustedIngestionSource(func(context.Context, string) (bool, error) { return false, nil })
	matcher := testSecretMatcher(t)
	executor.Secrets.SetSecretMatcher(matcher)

	deps := Deps{
		Boundary:      testBoundary(),
		SecretMatcher: matcher,
		SecretAsk:     executor.Secrets.AskSecretScreen,
	}

	makeJar := func() http.CookieJar {
		s := httpcookies.New(nil)
		for _, rawURL := range []string{originServer.URL, targetServer.URL} {
			u, parseErr := url.Parse(rawURL)
			testutil.FailErr(t, "parse server URL", parseErr)
			s.SetCookies(u, []*http.Cookie{
				{Name: "session", Value: cookieSecret, Path: "/"},
				{Name: "theme", Value: "dark", Path: "/"},
			})
		}
		return &storeBackedCookieJar{store: s}
	}

	originCap := loopbackCapability(t, originServer.URL)
	targetCap := loopbackCapability(t, targetServer.URL)
	bothPorts := append([]any(nil), originCap["loopback_connect"].(map[string]any)["ports"].([]any)...)
	bothPorts = append(bothPorts, targetCap["loopback_connect"].(map[string]any)["ports"].([]any)...)
	bothLoopbackCap := map[string]any{"loopback_connect": map[string]any{"ports": bothPorts}}

	runWithJar := func(callID, rawURL, redirects string) {
		t.Helper()
		tc := sessionContext(t.TempDir(), callID)
		ctx := secretmatch.WithAskAttribution(t.Context(), secretmatch.AskAttribution{
			SessionID:     tc.Identity.SessionID,
			RootSessionID: tc.ChatSessionID(),
			ProjectID:     tc.Identity.ProjectID,
			ProjectDir:    tc.ActiveRootPath(),
			ToolCallID:    callID,
		})
		ctx = secretcap.WithResolution(ctx, tc.Effects.Secrets)
		_, performErr := Perform(ctx, deps, map[string]any{
			"url":                rawURL,
			"redirects":          redirects,
			"capability_request": bothLoopbackCap,
		}, tc, ExchangeOptions{Jar: makeJar()})
		testutil.FailErr(t, callID, performErr)
	}

	// 1. Approve originServer with RecommendedOptionID (installs standing grant in real approval store).
	review.mode = "install_grant"
	runWithJar("call-1", originServer.URL+"/ok", "none")
	originGrantID := review.lastGrantID
	if review.cards != 1 || originGrantID == "" || originHits.Load() != 1 || !strings.Contains(originCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 1 cards=%d grantID=%q hits=%d cookie=%q", review.cards, originGrantID, originHits.Load(), originCookie.Load())
	}

	// 2. Quiet repeat to originServer under the standing grant raises 0 cards and sends the secret cookie.
	originHits.Store(0)
	originCookie.Store("")
	review.mode = "forbid_card"
	runWithJar("call-2", originServer.URL+"/ok", "none")
	if review.cards != 1 || originHits.Load() != 1 || !strings.Contains(originCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 2 quiet repeat cards=%d hits=%d cookie=%q", review.cards, originHits.Load(), originCookie.Load())
	}

	// Revoking the target's standing grant during the first hop makes the second hop ask again.
	// targetServer's standing grant during Hop 0; Hop 1 must re-ask against the live approval store and withhold
	// the secret cookie on rejection while completing the redirect.
	review.mode = "install_grant"
	runWithJar("call-3-seed-target", targetServer.URL+"/land", "none")
	targetGrantID = review.lastGrantID
	if review.cards != 2 || targetGrantID == "" || targetHits.Load() != 1 || !strings.Contains(targetCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 3 seed target cards=%d grantID=%q hits=%d cookie=%q", review.cards, targetGrantID, targetHits.Load(), targetCookie.Load())
	}

	originHits.Store(0)
	targetHits.Store(0)
	originCookie.Store("")
	targetCookie.Store("")
	revokeTargetOnRedirect.Store(true)
	review.mode = "withhold"
	runWithJar("call-3-redirect-revoke", originServer.URL+"/redirect", "safe")
	if review.cards != 3 {
		t.Fatalf("step 3 redirect after mid-flight target grant revocation cards=%d, want 3", review.cards)
	}
	if originHits.Load() != 1 || !strings.Contains(originCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 3 hop 0 hits=%d cookie=%q, want 1 with secret", originHits.Load(), originCookie.Load())
	}
	if targetHits.Load() != 1 || targetCookie.Load().(string) != "theme=dark" {
		t.Fatalf("step 3 hop 1 hits=%d cookie=%q, want 1 with only benign cookie theme=dark", targetHits.Load(), targetCookie.Load())
	}

	// A once approval covers one operation; the next one asks again.
	// and the next operation must re-ask again.
	removed, err := authority.RevokeGrant(originGrantID)
	testutil.FailErr(t, "revoke origin standing grant", err)
	if !removed {
		t.Fatal("expected origin standing grant to be removed")
	}
	originHits.Store(0)
	originCookie.Store("")
	review.mode = "approve_once"
	runWithJar("call-4-once", originServer.URL+"/ok", "none")
	if review.cards != 4 || originHits.Load() != 1 || !strings.Contains(originCookie.Load().(string), "session="+cookieSecret) {
		t.Fatalf("step 4 approve_once cards=%d hits=%d cookie=%q", review.cards, originHits.Load(), originCookie.Load())
	}

	originHits.Store(0)
	originCookie.Store("")
	review.mode = "withhold"
	runWithJar("call-5-after-once", originServer.URL+"/ok", "none")
	if review.cards != 5 || originHits.Load() != 1 || originCookie.Load().(string) != "theme=dark" {
		t.Fatalf("step 5 after once permit expired cards=%d hits=%d cookie=%q, want 5, 1, theme=dark", review.cards, originHits.Load(), originCookie.Load())
	}
}

type storeBackedCookieJar struct {
	store *httpcookies.Store
}

func (j *storeBackedCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.store.SetCookies(u, cookies)
}
func (j *storeBackedCookieJar) Cookies(u *url.URL) []*http.Cookie {
	return j.store.Cookies(u)
}
func (j *storeBackedCookieJar) CookieStore() *httpcookies.Store {
	return j.store
}

type cookieGrantReview struct {
	hitl.CheckpointManager
	t           *testing.T
	authority   hitl.ApprovalGate
	cards       int
	mode        string
	lastGrantID string
}

func (r *cookieGrantReview) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	r.cards++
	if r.mode == "forbid_card" {
		r.t.Fatalf("unexpected checkpoint card #%d raised during quiet repeat", r.cards)
	}
	if r.mode == "install_grant" {
		plan := req.ApprovalPlan
		if plan == nil {
			var err error
			plan, err = hitl.CompileCheckpointApprovalPlan(req)
			testutil.FailErr(r.t, "compile cookie secret review", err)
		}
		option, ok := plan.Option(plan.RecommendedOptionID)
		if !ok {
			r.t.Fatal("cookie secret review missing recommended option")
		}
		for _, delta := range option.Authority {
			if delta.Kind == hitl.AuthorityGenericGrant && delta.Grant != nil {
				_, err := r.authority.ApplyGrant(*delta.Grant)
				testutil.FailErr(r.t, "apply cookie secret grant", err)
				if delta.Grant.Predicate.Category == hitl.ApprovalGrantCategorySecret {
					r.lastGrantID = delta.Grant.ID
				}
			}
		}
	}
	return &hitl.CheckpointResponse{CheckpointID: "cookie-card", Status: hitl.DecisionStatusPending}, nil
}

func (r *cookieGrantReview) PollCheckpoint(context.Context, string) (*hitl.CheckpointResponse, error) {
	if r.mode == "withhold" {
		return &hitl.CheckpointResponse{Status: hitl.DecisionStatusRejected, Result: &hitl.DecisionResult{Approved: false}}, nil
	}
	return &hitl.CheckpointResponse{Status: hitl.DecisionStatusApproved, Result: &hitl.DecisionResult{Approved: true}}, nil
}

func (*cookieGrantReview) ApprovalReviewSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}

type unbackedCookieJar struct{}

func (unbackedCookieJar) SetCookies(*url.URL, []*http.Cookie) {}
func (unbackedCookieJar) Cookies(*url.URL) []*http.Cookie {
	return []*http.Cookie{{Name: "session", Value: "unreviewed"}}
}

// testFrontendJar is a caller-supplied jar over one cookie store, the shape
// curl's -b and -c jar hands the exchange.
type testFrontendJar struct{ store *httpcookies.Store }

func (j testFrontendJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.store.SetCookies(u, cookies)
}
func (j testFrontendJar) Cookies(u *url.URL) []*http.Cookie { return j.store.Cookies(u) }
func (j testFrontendJar) CookieStore() *httpcookies.Store   { return j.store }
