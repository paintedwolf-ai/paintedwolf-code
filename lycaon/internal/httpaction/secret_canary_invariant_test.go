package httpaction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// canaryValue differs under every encoding the canary checks.
const canaryValue = "canary-S3cr3t/+=&?#value ~"

const canaryRule = "tool results must never echo a resolved secret in any encoding; echo the reference, not the value"

// canaryReview approves every capability and secret card the executor raises,
// installing the reviewed grants so the request reaches the wire.
type canaryReview struct {
	hitl.CheckpointManager
	t         *testing.T
	authority hitl.ApprovalGate
	cards     int
	ports     []uint16
}

func (r *canaryReview) RequestCheckpoint(_ context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	r.cards++
	plan := req.ApprovalPlan
	if plan == nil {
		var err error
		plan, err = hitl.CompileCheckpointApprovalPlan(req)
		testutil.FailErr(r.t, "compile canary review", err)
	}
	if option, ok := plan.Option(plan.RecommendedOptionID); ok {
		for _, delta := range option.Authority {
			switch delta.Kind {
			case hitl.AuthorityGenericGrant:
				_, err := r.authority.ApplyGrant(*delta.Grant)
				testutil.FailErr(r.t, "install reviewed grant", err)
			case hitl.AuthorityLoopbackConnectChat:
				r.ports = append(r.ports, delta.ConnectPorts...)
			case hitl.AuthorityCurrentAction, hitl.AuthoritySocketPermit, hitl.AuthoritySocketChat,
				hitl.AuthorityDirectIPPermit, hitl.AuthorityDirectIPChat, hitl.AuthorityWriteRootChat,
				hitl.AuthorityReadPathChat, hitl.AuthorityLocalListenChat, hitl.AuthorityGrantedPath,
				hitl.AuthorityAskQuiet, hitl.AuthorityTrustDestination:
			}
		}
	}
	return &hitl.CheckpointResponse{CheckpointID: fmt.Sprint(r.cards), Status: hitl.DecisionStatusPending}, nil
}

func (r *canaryReview) PollCheckpoint(_ context.Context, id string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: id, Status: hitl.DecisionStatusApproved, Result: &hitl.DecisionResult{Approved: true}}, nil
}

func (r *canaryReview) Await(_ context.Context, ask tools.LoopbackConnectAsk) (tools.LoopbackConnectResult, error) {
	r.cards++
	r.ports = append(r.ports, ask.Ports...)
	return tools.LoopbackConnectResult{Raised: true, Authorized: true, SecretApproved: true, Ports: ask.Ports}, nil
}

func (r *canaryReview) SessionLoopbackGrant(context.Context, string, string) (bool, []uint16) {
	return len(r.ports) > 0, append([]uint16(nil), r.ports...)
}

// canaryForms names every encoding of the canary a result must not carry.
func canaryForms(value string) map[string]string {
	return map[string]string{
		"plaintext":                value,
		"base64(plaintext)":        base64.StdEncoding.EncodeToString([]byte(value)),
		"base64url(plaintext)":     base64.URLEncoding.EncodeToString([]byte(value)),
		"base64(user:plaintext)":   base64.StdEncoding.EncodeToString([]byte("user:" + value)),
		"url.QueryEscape":          url.QueryEscape(value),
		"url.PathEscape":           url.PathEscape(value),
		"json-escaped plaintext":   strings.Trim(mustJSON(value), `"`),
		"json-escaped QueryEscape": strings.Trim(mustJSON(url.QueryEscape(value)), `"`),
	}
}

func mustJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// leafStrings flattens a decoded result into field path → string value.
func leafStrings(prefix string, v any, out map[string]string) {
	switch node := v.(type) {
	case map[string]any:
		for key, child := range node {
			leafStrings(joinPath(prefix, key), child, out)
		}
	case []any:
		for i, child := range node {
			leafStrings(fmt.Sprintf("%s[%d]", prefix, i), child, out)
		}
	case string:
		out[prefix] = node
	}
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// assertNoCanary fails naming the tool, scheme, encoded form, and field.
func assertNoCanary(t *testing.T, tool, scheme, out string) {
	t.Helper()
	forms := canaryForms(canaryValue)
	names := make([]string, 0, len(forms))
	for name := range forms {
		names = append(names, name)
	}
	sort.Strings(names)
	fields := map[string]string{}
	var decoded any
	if err := json.Unmarshal([]byte(out), &decoded); err == nil {
		leafStrings("", decoded, fields)
	}
	paths := make([]string, 0, len(fields))
	for path := range fields {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, name := range names {
		form := forms[name]
		for _, path := range paths {
			if strings.Contains(fields[path], form) {
				t.Errorf("%s (%s): result field %q carries the %s form of the resolved secret: %q; %s",
					tool, scheme, path, name, fields[path], canaryRule)
			}
		}
		if strings.Contains(out, form) {
			t.Errorf("%s (%s): raw result carries the %s form of the resolved secret; %s", tool, scheme, name, canaryRule)
		}
	}
}

func TestSecretCanaryNeverEchoedByHTTPRequestResult(t *testing.T) {
	service, _, database := newTestSecrets(t)
	meta, err := service.CreateSettingsSecret(t.Context(), secretcap.CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testdbseed.OwnerID(t, database), OperationID: "canary", Name: "Canary", Purpose: "secret canary", Value: canaryValue,
	})
	testutil.FailErr(t, "create canary secret", err)

	var wire []string
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		wire = append(wire, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Api-Key")+"|"+r.Header.Get("Cookie"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		wire = append(wire, r.URL.Query().Get("key"))
		http.Redirect(w, r, "/final?echo="+url.QueryEscape(r.URL.Query().Get("key"))+"&page=2", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/form", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "csrftoken", Value: url.QueryEscape(canaryValue), Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		wire = append(wire, r.Header.Get("X-Csrftoken"))
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approvals", err)
	authority := settings.NewRuleApprovalGate(store, settings.NoSources())
	review := &canaryReview{t: t, authority: authority}
	registry := tools.NewDefaultRegistry()
	executor := tools.NewDefaultToolExecutor(nil, registry, "implement")
	executor.SetCheckpointManager(review, authority)
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	executor.SetSecretResolver(service)
	matcher := testSecretMatcher(t)
	executor.SetSecretMatcher(matcher)
	executor.SetLoopbackConnectGate(review)
	executor.SetSessionLoopbackGrant(review.SessionLoopbackGrant)
	testutil.FailErr(t, "register http_request", Register(registry, Deps{
		Boundary: testBoundary(), SecretMatcher: matcher, SecretAsk: executor.AskSecretScreen, Secrets: service,
	}))
	root := t.TempDir()
	capability := loopbackCapability(t, server.URL)

	for _, tc := range []struct {
		scheme string
		prime  map[string]any
		args   map[string]any
		wire   string
	}{
		{scheme: "bearer", wire: "Bearer " + canaryValue + "||", args: map[string]any{
			"url": server.URL + "/echo", "auth": map[string]any{"scheme": "bearer", "token": meta.Reference},
		}},
		{scheme: "basic", wire: "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+canaryValue)) + "||", args: map[string]any{
			"url": server.URL + "/echo", "auth": map[string]any{"scheme": "basic", "username": "user", "password": meta.Reference},
		}},
		{scheme: "header reference", wire: "|" + canaryValue + "|", args: map[string]any{
			"url": server.URL + "/echo", "headers": []any{map[string]any{"name": "X-Api-Key", "value": meta.Reference}},
		}},
		{scheme: "cookie header reference", wire: "||session=" + canaryValue, args: map[string]any{
			"url": server.URL + "/echo", "headers": []any{map[string]any{"name": "Cookie", "value": "session=" + meta.Reference}},
		}},
		{scheme: "jar cookie reference", wire: url.QueryEscape(canaryValue),
			prime: map[string]any{"url": server.URL + "/form", "cookie_jar": "app"},
			args: map[string]any{
				"url": server.URL + "/submit", "method": "POST", "cookie_jar": "app",
				"headers": []any{map[string]any{"name": "X-CSRFToken", "value": "{{cookie:csrftoken}}"}},
			}},
		{scheme: "query reference with redirect", wire: canaryValue, args: map[string]any{
			"url": server.URL + "/start", "redirects": "safe",
			"query": []any{map[string]any{"name": "key", "value": meta.Reference}},
		}},
	} {
		t.Run(tc.scheme, func(t *testing.T) {
			call := strings.ReplaceAll(tc.scheme, " ", "-")
			tctx := sessionContext(root, call)
			if tc.prime != nil {
				tc.prime["capability_request"] = capability
				tctx.ToolCallID = call + "-prime"
				primed, err := executor.Invoke(t.Context(), "http_request", tc.prime, tctx)
				testutil.FailErr(t, "prime cookie jar", err)
				assertNoCanary(t, "http_request", tc.scheme+" (prime)", primed)
				tctx.ToolCallID = call
			}
			tc.args["capability_request"] = capability
			sent := len(wire)
			out, err := executor.Invoke(t.Context(), "http_request", tc.args, tctx)
			testutil.FailErr(t, "invoke http_request with the canary", err)
			if len(wire) <= sent || wire[sent] != tc.wire {
				t.Fatalf("http_request (%s): the server did not receive the resolved canary; wire=%q", tc.scheme, wire[sent:])
			}
			assertNoCanary(t, "http_request", tc.scheme, out)
		})
	}
}
