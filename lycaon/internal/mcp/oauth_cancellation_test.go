package mcp

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func cancellationFixture(t *testing.T) (*OAuthClient, *OAuthTokenStore, <-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"late-token","token_type":"Bearer"}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(unblock)
	store := NewOAuthTokenStoreAt(t.TempDir() + "/oauth.yaml")
	client := NewOAuthClient(store, "", srv.Client())
	t.Cleanup(client.Close)
	client.pending["remote"] = pendingOAuth{
		ProviderID: "remote", State: "first", ClientID: "fixture", Verifier: "verifier",
		TokenURL: srv.URL, Resource: srv.URL + "/mcp", CreatedAt: time.Now(),
	}
	return client, store, entered, unblock
}

func TestOAuthCancellationClosesOnlyNamedListener(t *testing.T) {
	client, store, _, _ := cancellationFixture(t)
	testutil.FailErr(t, "save existing credentials", store.Put("remote", OAuthTokenRecord{AccessToken: "existing"}))
	listener, err := startCallbackListener(func(callbackResult) error { return nil })
	testutil.FailErr(t, "start callback", err)
	pending := client.pending["remote"]
	pending.listener = listener
	client.pending["remote"] = pending
	client.pending["other"] = pendingOAuth{State: "other", CreatedAt: time.Now()}

	client.Cancel("remote", "", "wrong")
	client.Cancel("remote", "", "")
	select {
	case <-listener.done:
		t.Fatal("unrelated cancellation closed listener")
	default:
	}
	client.Cancel("remote", "", "first")
	client.Cancel("remote", "", "first")
	select {
	case <-listener.done:
	default:
		t.Fatal("cancellation did not stop listener")
	}
	if _, ok := client.pending["remote"]; ok {
		t.Fatal("canceled attempt remains pending")
	}
	if _, ok := client.pending["other"]; !ok {
		t.Fatal("cancellation removed another provider's attempt")
	}
	if token, ok := store.Get("remote"); !ok || token.AccessToken != "existing" {
		t.Fatal("cancellation changed existing credentials")
	}
	if err := client.Complete(t.Context(), "remote", "", OAuthCompleteRequest{State: "first", Code: "late"}); err == nil {
		t.Fatal("canceled attempt accepted a callback")
	}
}

func TestOAuthLateExchangeCannotPublishAfterRetirement(t *testing.T) {
	for _, action := range []string{"cancel", "replace", "revoke", "close"} {
		t.Run(action, func(t *testing.T) {
			client, store, entered, unblock := cancellationFixture(t)
			testutil.FailErr(t, "save existing credentials", store.Put("remote", OAuthTokenRecord{AccessToken: "existing"}))
			result := make(chan error, 1)
			go func() {
				result <- client.Complete(t.Context(), "remote", "", OAuthCompleteRequest{State: "first", Code: "code"})
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("exchange did not reach token endpoint")
			}
			switch action {
			case "cancel":
				client.Cancel("remote", "", "first")
			case "replace":
				client.mu.Lock()
				pending := client.pending["remote"]
				pending.State = "second"
				client.pending["remote"] = pending
				client.mu.Unlock()
				client.Cancel("remote", "", "first")
			case "revoke":
				testutil.FailErr(t, "revoke credentials", client.Revoke("remote"))
			case "close":
				client.Close()
			}
			unblock()
			if err := <-result; err == nil {
				t.Fatal("retired authorization published late tokens")
			}
			token, ok := store.Get("remote")
			if action == "revoke" {
				if ok {
					t.Fatal("late exchange restored revoked credentials")
				}
			} else if !ok || token.AccessToken != "existing" {
				t.Fatal("late exchange replaced saved credentials")
			}
			if action == "replace" {
				if client.pending["remote"].State != "second" {
					t.Fatal("old exchange retired newer attempt")
				}
				testutil.FailErr(t, "complete newer attempt", client.Complete(t.Context(), "remote", "", OAuthCompleteRequest{State: "second", Code: "new-code"}))
				if saved, ok := store.Get("remote"); !ok || saved.AccessToken != "late-token" {
					t.Fatal("newer attempt did not save credentials")
				}
			}
		})
	}
}

func TestOAuthTokenPublicationIsSingleUse(t *testing.T) {
	client, store, _, _ := cancellationFixture(t)
	pending := client.pending["remote"]
	testutil.FailErr(t, "publish first exchange", client.commitAuthorization("remote", pending, OAuthTokenRecord{AccessToken: "first-token"}))
	if err := client.commitAuthorization("remote", pending, OAuthTokenRecord{AccessToken: "replayed-token"}); err == nil {
		t.Fatal("duplicate exchange published twice")
	}
	if token, ok := store.Get("remote"); !ok || token.AccessToken != "first-token" {
		t.Fatal("duplicate exchange replaced first token")
	}
}
