package httpaction

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// csrfServer is the double-submit pattern: a token arrives as a cookie and has
// to come back in a header the client never gets to read from the jar.
func csrfServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var accepted []string
	mux := http.NewServeMux()
	mux.HandleFunc("/form", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "csrftoken", Value: "token-abc123", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "session-xyz789", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("csrftoken")
		if err != nil || r.Header.Get("X-CSRFToken") != cookie.Value {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		accepted = append(accepted, r.Header.Get("X-CSRFToken"))
		w.WriteHeader(http.StatusCreated)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &accepted
}

func TestCookieReferenceEchoesATokenTheModelNeverSees(t *testing.T) {
	secrets, _ := testSecrets(t)
	server, accepted := csrfServer(t)
	deps := Deps{Boundary: testBoundary(), Secrets: secrets}
	root := t.TempDir()

	primed, err := runRequest(t, deps, map[string]any{
		"url": server.URL + "/form", "cookie_jar": "app",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "prime the jar", err)
	if primed.Cookies == nil || !primed.Cookies.Persisted {
		t.Fatalf("cookies = %+v", primed.Cookies)
	}
	if strings.Join(primed.Cookies.Names, ",") != "csrftoken,sessionid" {
		t.Fatalf("names = %v, want the held cookie names", primed.Cookies.Names)
	}

	out, err := runRequest(t, deps, map[string]any{
		"url": server.URL + "/submit", "method": "POST", "cookie_jar": "app",
		"headers":            []any{map[string]any{"name": "X-CSRFToken", "value": "{{cookie:csrftoken}}"}},
		"body_text":          "field=1",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-2"))
	testutil.FailErr(t, "submit with the reference", err)
	if out.Status != http.StatusCreated {
		t.Fatalf("status = %d, want the double-submit to be accepted", out.Status)
	}
	if len(*accepted) != 1 || (*accepted)[0] != "token-abc123" {
		t.Fatalf("server saw %v", *accepted)
	}
	// The value went on the wire and nowhere else.
	if strings.Contains(out.Body, "token-abc123") {
		t.Fatal("the cookie value came back in the result")
	}
	if len(out.SentHeaders) == 0 || out.SentHeaders[0].Name != "X-CSRFToken" || out.SentHeaders[0].Value != "{{cookie:csrftoken}}" {
		t.Fatalf("sent_headers = %+v, want the cookie reference rather than its value", out.SentHeaders)
	}
}

func TestCookieReferenceInABodyIsPlacedToo(t *testing.T) {
	secrets, _ := testSecrets(t)
	var seenBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/form", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "csrftoken", Value: "token-abc123", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 256)
		n, _ := r.Body.Read(body)
		seenBody = string(body[:n])
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	deps := Deps{Boundary: testBoundary(), Secrets: secrets}
	root := t.TempDir()
	_, err := runRequest(t, deps, map[string]any{
		"url": server.URL + "/form", "cookie_jar": "app",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "prime the jar", err)

	_, err = runRequest(t, deps, map[string]any{
		"url": server.URL + "/submit", "method": "POST", "cookie_jar": "app",
		"body_text":          "csrfmiddlewaretoken={{cookie:csrftoken}}&field=1",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-2"))
	testutil.FailErr(t, "submit", err)
	if seenBody != "csrfmiddlewaretoken=token-abc123&field=1" {
		t.Fatalf("body = %q", seenBody)
	}
}

func TestUnheldCookieReferenceIsRefusedNotSentLiterally(t *testing.T) {
	secrets, _ := testSecrets(t)
	server, _ := csrfServer(t)
	deps := Deps{Boundary: testBoundary(), Secrets: secrets}
	root := t.TempDir()
	_, err := runRequest(t, deps, map[string]any{
		"url": server.URL + "/form", "cookie_jar": "app",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "prime the jar", err)

	_, err = runRequest(t, deps, map[string]any{
		"url": server.URL + "/submit", "method": "POST", "cookie_jar": "app",
		"headers": []any{
			map[string]any{"name": "X-CSRFToken", "value": "{{cookie:xsrf}}"},
			map[string]any{"name": "X-Session", "value": "{{cookie:sid}}"},
		},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-2"))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != CookieNotHeldCode {
		t.Fatalf("error = %v, want %s", err, CookieNotHeldCode)
	}
	missing, _ := reject.Data["missing"].([]string)
	if strings.Join(missing, ",") != "sid,xsrf" {
		t.Fatalf("missing = %v, want every name that was not held", reject.Data["missing"])
	}
	held, _ := reject.Data["held"].([]string)
	if strings.Join(held, ",") != "csrftoken,sessionid" {
		t.Fatalf("held = %v, want the names the jar does carry", reject.Data["held"])
	}
}

func TestJarThatCannotBePersistedDoesNotDiscardTheExchange(t *testing.T) {
	secrets, database := testSecretsWithStore(t)
	var served int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "session-xyz789", Path: "/"})
		_, _ = w.Write([]byte("created"))
		// The store goes away after the jar was opened and the order was
		// placed: the shape of a secret store that becomes unavailable
		// mid-request.
		testutil.FailErr(t, "close the managed secret store", database.Close())
	}))
	t.Cleanup(server.Close)

	out, err := runRequest(t, Deps{Boundary: testBoundary(), Secrets: secrets}, map[string]any{
		"url": server.URL + "/orders", "method": "POST", "body_text": "item=1", "cookie_jar": "app",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(t.TempDir(), "call-1"))
	// The order was placed. Rejecting here would hide that and invite a retry.
	testutil.FailErr(t, "post with an unwritable jar", err)
	if served != 1 || out.Status != http.StatusOK || out.Body != "created" {
		t.Fatalf("served=%d result=%+v", served, out)
	}
	if out.Cookies == nil || out.Cookies.Persisted || out.Cookies.Error == "" {
		t.Fatalf("cookies = %+v, want an admitted persistence failure", out.Cookies)
	}
}

func TestCookieReferenceWithoutAJarIsRefused(t *testing.T) {
	_, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url":     "https://example.test/",
		"headers": []any{map[string]any{"name": "X-CSRFToken", "value": "{{cookie:csrftoken}}"}},
	}, sessionContext(t.TempDir(), "call-1"))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("error = %v, want TOOL_ARGS_INVALID", err)
	}
}

func TestJarIsWrittenBackWhenTheExchangeFails(t *testing.T) {
	secrets, _ := testSecrets(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sessionid", Value: "session-xyz789", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		// The login succeeded; only the body is past the bound.
		_, _ = w.Write(make([]byte, (5<<20)+1))
	}))
	t.Cleanup(server.Close)

	deps := Deps{Boundary: testBoundary(), Secrets: secrets}
	root := t.TempDir()
	_, err := runRequest(t, deps, map[string]any{
		"url": server.URL + "/login", "method": "POST", "body_text": "u=1", "cookie_jar": "app",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "HTTP_REQUEST_FAILED" {
		t.Fatalf("error = %v", err)
	}
	receipt, ok := reject.Data["cookies"].(map[string]any)
	if !ok || receipt["stored"] != 1 || receipt["persisted"] != true {
		t.Fatalf("reject cookies = %v, want the session the failed exchange still established", reject.Data["cookies"])
	}

	// The session survives into the next call rather than being lost with the
	// body that overflowed.
	out, err := runRequest(t, deps, map[string]any{
		"url": server.URL + "/me", "response_body": "discard", "cookie_jar": "app",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-2"))
	testutil.FailErr(t, "second call", err)
	if out.Cookies == nil || out.Cookies.Sent != 1 {
		t.Fatalf("cookies = %+v, want the stored session sent on the next call", out.Cookies)
	}
}

func TestCookieRecoveryNamesMatchDestinationWithoutSending(t *testing.T) {
	source, err := url.Parse("https://example.test/")
	testutil.FailErr(t, "parse source URL", err)
	other, err := url.Parse("https://other.test/")
	testutil.FailErr(t, "parse other URL", err)
	target, err := url.Parse("http://example.test/app")
	testutil.FailErr(t, "parse target URL", err)
	store := httpcookies.New(nil)
	store.SetCookies(source, []*http.Cookie{
		{Name: "public", Value: "public-secret", Path: "/"},
		{Name: "secure", Value: "secure-secret", Path: "/", Secure: true},
		{Name: "admin", Value: "admin-secret", Path: "/admin"},
	})
	store.SetCookies(other, []*http.Cookie{{Name: "other", Value: "other-secret", Path: "/"}})
	before, _ := store.Counts()
	out, reject := resolveCookieReferences(outboundRequest{body: []byte("{{cookie:missing}}")}, &secretcap.CookieJar{Name: "fixture", Store: store}, target)
	if reject == nil || reject.Code != CookieNotHeldCode || len(out.body) != 0 {
		t.Fatalf("unheld cookie request escaped: out=%+v reject=%+v", out, reject)
	}
	if !reflect.DeepEqual(reject.Data["held"], []string{"public"}) {
		t.Fatalf("unusable destination cookies advertised: %#v", reject.Data)
	}
	after, _ := store.Counts()
	if after != before {
		t.Fatal("recovery observation counted as cookie traffic")
	}
}
