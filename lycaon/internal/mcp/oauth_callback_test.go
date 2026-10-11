package mcp

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func waitForCallback(t *testing.T, got <-chan callbackResult) callbackResult {
	t.Helper()
	select {
	case res := <-got:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("callback listener never fired")
		return callbackResult{}
	}
}

// The browser lands on a live loopback listener and the host reads the code off the
// redirect.
func TestCallbackListenerReceivesCode(t *testing.T) {
	got := make(chan callbackResult, 1)
	l, err := startCallbackListener(t.Context(), func(res callbackResult) error {
		got <- res
		return nil
	})
	testutil.FailErr(t, "startCallbackListener failed", err)
	t.Cleanup(func() { l.finish(t.Context()) })

	if !strings.HasPrefix(l.RedirectURI(), "http://127.0.0.1:") {
		t.Fatalf("redirect = %q want an IPv4 loopback URL", l.RedirectURI())
	}
	if !strings.HasSuffix(l.RedirectURI(), oauthCallbackPath) {
		t.Fatalf("redirect = %q missing callback path", l.RedirectURI())
	}

	resp, err := http.Get(l.RedirectURI() + "?code=abc123&state=st-1")
	testutil.FailErr(t, "http.Get failed", err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", resp.StatusCode)
	}

	res := waitForCallback(t, got)
	if res.Code != "abc123" || res.State != "st-1" {
		t.Fatalf("callback = %+v", res)
	}
}

// response_mode=form_post delivers the code in a POST body rather than the query.
func TestCallbackListenerAcceptsFormPost(t *testing.T) {
	got := make(chan callbackResult, 1)
	l, err := startCallbackListener(t.Context(), func(res callbackResult) error {
		got <- res
		return nil
	})
	testutil.FailErr(t, "startCallbackListener failed", err)
	t.Cleanup(func() { l.finish(t.Context()) })

	form := url.Values{"code": {"posted"}, "state": {"st-2"}}
	resp, err := http.Post(l.RedirectURI(), "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	testutil.FailErr(t, "http.Post failed", err)
	defer func() { _ = resp.Body.Close() }()

	res := waitForCallback(t, got)
	if res.Code != "posted" || res.State != "st-2" {
		t.Fatalf("callback = %+v", res)
	}
}

// A user who declines at the consent screen is redirected with an error, not a code.
func TestCallbackListenerSurfacesAuthorizationError(t *testing.T) {
	got := make(chan callbackResult, 1)
	l, err := startCallbackListener(t.Context(), func(res callbackResult) error {
		got <- res
		return nil
	})
	testutil.FailErr(t, "startCallbackListener failed", err)
	t.Cleanup(func() { l.finish(t.Context()) })

	resp, err := http.Get(l.RedirectURI() + "?error=access_denied&error_description=user+declined&state=st-3")
	testutil.FailErr(t, "http.Get failed", err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("declined callback status = %d want 400", resp.StatusCode)
	}

	// The handler is never invoked for an authorization error: there is no code to
	// redeem, so nothing should reach the exchange.
	select {
	case res := <-got:
		t.Fatalf("handler ran for an authorization error: %+v", res)
	case <-time.After(200 * time.Millisecond):
	}
}

// The listener is one-shot. A second redirect carrying a replayed code must not find
// anything listening.
func TestCallbackListenerStopsAfterFirstRedirect(t *testing.T) {
	l, err := startCallbackListener(t.Context(), func(callbackResult) error { return nil })
	testutil.FailErr(t, "startCallbackListener failed", err)
	t.Cleanup(func() { l.finish(t.Context()) })
	redirect := l.RedirectURI()

	resp, err := http.Get(redirect + "?code=one&state=st")
	testutil.FailErr(t, "http.Get failed", err)
	_ = resp.Body.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp2, err := http.Get(redirect + "?code=two&state=st")
		if err != nil {
			return // listener is down, which is the assertion
		}
		_ = resp2.Body.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("listener still accepting redirects after the first one completed")
}

// A redirect that does not carry the state this authorization issued is not this
// authorization, and must not redeem a code. The loopback port is reachable by anything
// on the machine, so this check lives at the handler, not only in Complete.
func TestCallbackListenerRejectsStateMismatch(t *testing.T) {
	store := NewOAuthTokenStoreAt(t.TempDir() + "/oauth.yaml")
	client := NewOAuthClient(store, "", nil)

	var completed error
	completedCalled := false
	listener, redirect, err := client.bindCallback(t.Context(), "srv", "", "expected-state", func(err error) {
		completed = err
		completedCalled = true
	})
	testutil.FailErr(t, "client.bindCallback failed", err)
	t.Cleanup(func() { listener.finish(t.Context()) })

	resp, err := http.Get(redirect + "?code=abc&state=attacker-state")
	testutil.FailErr(t, "http.Get failed", err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("state mismatch status = %d want 400", resp.StatusCode)
	}
	if completedCalled {
		t.Fatalf("a mismatched state reached the exchange: %v", completed)
	}
	if store.SignedIn("srv") {
		t.Fatal("tokens stored for a mismatched state")
	}
}

// A pending authorization's PKCE verifier and state expire, so an abandoned sign-in
// does not stay redeemable.
func TestPendingOAuthExpires(t *testing.T) {
	store := NewOAuthTokenStoreAt(t.TempDir() + "/oauth.yaml")
	client := NewOAuthClient(store, "http://127.0.0.1:1/callback", nil)

	now := time.Now()
	client.now = func() time.Time { return now }
	client.mu.Lock()
	client.pending["srv"] = pendingOAuth{ProviderID: "srv", State: "st", CreatedAt: now}
	client.mu.Unlock()

	now = now.Add(pendingOAuthTTL + time.Minute)
	err := client.Complete(t.Context(), "srv", "", OAuthCompleteRequest{Code: "c", State: "st"})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Complete on an expired authorization = %v want expiry", err)
	}
	client.mu.Lock()
	_, still := client.pending["srv"]
	client.mu.Unlock()
	if still {
		t.Fatal("expired authorization stayed pending")
	}
}
