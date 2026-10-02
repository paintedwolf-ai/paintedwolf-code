package httpaction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestSentHeadersStateReferencesAndSchemeNotCredentials(t *testing.T) {
	const value = "managed-opaque-credential"
	service, _ := managedRequestService(t)
	meta := hostSecret(t, service, "create", "Token", value)
	var wire []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire = append(wire, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	for _, tc := range []struct {
		name       string
		auth       map[string]any
		wantScheme string
		wireValue  string
	}{
		{"bearer", map[string]any{"scheme": "bearer", "token": meta.Reference}, "Bearer", "Bearer " + value},
		{"basic", map[string]any{"scheme": "basic", "username": "user", "password": meta.Reference}, "Basic",
			"Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+value))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{
				"url": server.URL, "auth": tc.auth,
				"headers":            []any{map[string]any{"name": "X-Api-Key", "value": meta.Reference}},
				"capability_request": loopbackCapability(t, server.URL),
			}
			registry := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register HTTP", Register(registry, Deps{Boundary: testBoundary(), SecretMatcher: testSecretMatcher(t),
				SecretAsk: func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
					return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
				},
			}))
			resolved, err := service.Resolve(t.Context(), args, secretcap.ResolveContext{ProjectID: testdbseed.DefaultProjectID, ToolName: "http_request", ToolCallID: tc.name})
			testutil.FailErr(t, "resolve request", err)
			out, err := registry.Run(t.Context(), "http_request", resolved.Arguments, tools.ToolContext{CanonicalArgs: args, Secrets: resolved})
			resolved.Finish(t.Context())
			testutil.FailErr(t, "send request", err)

			if wire[len(wire)-1] != tc.wireValue {
				t.Fatalf("wire Authorization = %q, want the resolved credential", wire[len(wire)-1])
			}
			encoded := base64.StdEncoding.EncodeToString([]byte("user:" + value))
			if strings.Contains(out, value) || strings.Contains(out, encoded) {
				t.Fatal("the credential or its encoding came back in the result")
			}
			var got result
			testutil.FailErr(t, "decode result", json.Unmarshal([]byte(out), &got))
			want := []outboundhttp.Header{{Name: "X-Api-Key", Value: meta.Reference}, {Name: "Authorization", Value: tc.wantScheme}}
			if len(got.SentHeaders) != len(want) || got.SentHeaders[0] != want[0] || got.SentHeaders[1] != want[1] {
				t.Fatalf("sent_headers = %+v, want %+v", got.SentHeaders, want)
			}
		})
	}
}

func TestFinalURLStatesSecretReferencesAndKeepsTheRedirectChain(t *testing.T) {
	const value = "managed-query-credential+/="
	service, _ := managedRequestService(t)
	meta := hostSecret(t, service, "create", "Key", value)
	var landed string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final?echo="+url.QueryEscape(r.URL.Query().Get("key"))+"&page=2", http.StatusFound)
			return
		}
		landed = r.URL.Query().Get("echo")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	args := map[string]any{
		"url": server.URL + "/start", "redirects": "safe",
		"query":              []any{map[string]any{"name": "key", "value": meta.Reference}},
		"capability_request": loopbackCapability(t, server.URL),
	}
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register HTTP", Register(registry, Deps{Boundary: testBoundary(), SecretMatcher: testSecretMatcher(t),
		SecretAsk: func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
		},
	}))
	resolved, err := service.Resolve(t.Context(), args, secretcap.ResolveContext{ProjectID: testdbseed.DefaultProjectID, ToolName: "http_request", ToolCallID: "final-url"})
	testutil.FailErr(t, "resolve request", err)
	out, err := registry.Run(t.Context(), "http_request", resolved.Arguments, tools.ToolContext{CanonicalArgs: args, Secrets: resolved})
	resolved.Finish(t.Context())
	testutil.FailErr(t, "send request", err)

	if landed != value {
		t.Fatalf("server saw %q, want the resolved value to reach it", landed)
	}
	if strings.Contains(out, value) || strings.Contains(out, url.QueryEscape(value)) {
		t.Fatalf("the query credential came back in the result: %s", out)
	}
	var got result
	testutil.FailErr(t, "decode result", json.Unmarshal([]byte(out), &got))
	if want := server.URL + "/final?echo=" + meta.Reference + "&page=2"; got.FinalURL != want {
		t.Fatalf("final_url = %q, want %q", got.FinalURL, want)
	}
	if len(got.Redirects) != 1 {
		t.Fatalf("redirects = %+v, want one observed hop", got.Redirects)
	}
	hop := got.Redirects[0]
	if hop.Status != http.StatusFound || hop.URL != server.URL+"/start?key="+meta.Reference ||
		hop.Location != server.URL+"/final?echo="+meta.Reference+"&page=2" {
		t.Fatalf("redirect hop = %+v, want the observed chain with the reference in place of the value", hop)
	}
}
