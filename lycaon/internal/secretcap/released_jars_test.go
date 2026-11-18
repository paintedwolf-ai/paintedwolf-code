package secretcap

import (
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleasedHTTPJarsReopenAfterReconciliation(t *testing.T) {
	service, values, _ := testService(t)
	ctx := t.Context()
	cookieRequest := jarRequest("open-cookie")
	tokenRequest := tokenJarRequest("open-token")
	seed := func(name, origin, purpose, document string) string {
		t.Helper()
		result, err := service.Put(ctx, PutRequest{
			ProjectID: cookieRequest.ProjectID, ChatSessionID: cookieRequest.ChatSessionID,
			SessionID: cookieRequest.SessionID, OperationID: "seed-" + name,
			Scope: ScopeChat, Name: name, Origin: origin, Purpose: purpose, Value: document,
		})
		testutil.FailErr(t, "store released jar", err)
		return result.Metadata.Reference
	}
	// Freeze the v1.0.2 vault documents independently of the current encoders.
	cookieRef := seed(cookieRequest.Name, OriginCookieJar, cookieJarPurpose,
		`{"cookies":[{"name":"sid","value":"fixture-cookie","domain":"service.example","host_only":true,"path":"/private","secure":true,"http_only":true,"created":"2026-01-01T00:00:00Z"}]}`)
	tokenRef := seed(tokenRequest.Name, OriginTokenJar, tokenJarPurpose,
		`{"session":{"value":"fixture-token","origin":"service.example@fixture","origin_label":"https://service.example:443"}}`)

	reopened := NewWithStore(service.repository.handle, values, nil)
	testutil.FailErr(t, "reconcile vault", reopened.Reconcile(ctx))
	assertDurableValues(t, reopened, "fixture-cookie", "fixture-token")
	cookies, err := reopened.Jars.OpenCookieJar(ctx, cookieRequest)
	testutil.FailErr(t, "open released cookies", err)
	target := &url.URL{Scheme: "https", Host: "service.example", Path: "/private"}
	selected := cookies.Store.Selected(target)
	if cookies.Reference != cookieRef || len(selected) != 1 || selected[0].Value != "fixture-cookie" ||
		!selected[0].HTTPOnly || selected[0].NativeIssuer != "" {
		t.Fatal("released cookie lost its reference, value, attributes or issuer")
	}
	target.Scheme = "http"
	if len(cookies.Store.Selected(target)) != 0 {
		t.Fatal("secure cookie selected for cleartext")
	}
	tokens, err := reopened.Jars.OpenTokenJar(ctx, tokenRequest)
	testutil.FailErr(t, "open released tokens", err)
	token, ok := tokens.Lookup("session")
	if !ok || tokens.Reference != tokenRef || token != (Token{Value: "fixture-token", Origin: "service.example@fixture", OriginLabel: "https://service.example:443"}) {
		t.Fatal("released token lost its reference, value or issuer")
	}
}
