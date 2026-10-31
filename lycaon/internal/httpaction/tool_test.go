package httpaction

import (
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
)

func testBoundary() *sandbox.Boundary {
	return sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"http_request": true},
	}})
}

func loopbackCapability(t *testing.T, rawURL string) map[string]any {
	t.Helper()
	target, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	port, err := strconv.Atoi(target.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return map[string]any{"loopback_connect": map[string]any{"ports": []any{port}}}
}

func TestHTTPRequestSendsJSONAndReturnsStructuredResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Test") != "yes" {
			t.Errorf("request = %s content-type=%q x-test=%q", r.Method, r.Header.Get("Content-Type"), r.Header.Get("X-Test"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["ok"] != true {
			t.Errorf("json body = %v err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(server.Close)

	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	outcome := &tools.ToolInvocationOut{}
	out, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url": server.URL + "/v1/jobs", "method": "POST", "body_json": map[string]any{"ok": true},
		"headers":            []any{map[string]any{"name": "X-Test", "value": "yes"}},
		"capability_request": loopbackCapability(t, server.URL),
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
		Effects:  tools.InvocationEffects{Out: outcome},
	})
	if err != nil {
		t.Fatalf("run http_request: %v", err)
	}
	var got result
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode http response: %v", err)
	}
	if got.Status != http.StatusAccepted || got.Body != `{"accepted":true}` || got.BodyEncoding != "utf-8" || outcome.RetrievedFrom != "127.0.0.1" {
		t.Fatalf("response = %+v outcome=%+v", got, outcome)
	}
}

func TestRequestBodyReadsProjectFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload.bin"), []byte("payload"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	body, err := assembleBody(t.Context(), testBoundary(), tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "root"},
	}, map[string]any{"body_path": "payload.bin"})
	if err != nil {
		t.Fatalf("assembleBody: %v", err)
	}
	if string(body.bytes) != "payload" || body.sourcePath != "payload.bin" {
		t.Fatalf("body=%q source=%q", body.bytes, body.sourcePath)
	}
}

func TestRequestBodyRejectsMultipleSources(t *testing.T) {
	_, err := assembleBody(t.Context(), testBoundary(), tools.ToolContext{}, map[string]any{
		"body_text": "text", "body_json": map[string]any{"ok": true},
	})
	if err == nil {
		t.Fatal("multiple body sources were accepted")
	}
}

func TestRequestBodyRejectsSerializedJSONText(t *testing.T) {
	_, err := assembleBody(t.Context(), testBoundary(), tools.ToolContext{}, map[string]any{
		"body_json": `{"ok":true}`,
	})
	if err == nil || !strings.Contains(err.Error(), "object or array") {
		t.Fatalf("assembleBody error = %v, want object-or-array validation", err)
	}
}

func TestRequestBodyAcceptsJSONArray(t *testing.T) {
	body, err := assembleBody(t.Context(), testBoundary(), tools.ToolContext{}, map[string]any{
		"body_json": []any{"first", map[string]any{"ok": true}},
	})
	if err != nil {
		t.Fatalf("assembleBody: %v", err)
	}
	if string(body.bytes) != `["first",{"ok":true}]` || body.contentType != "application/json" {
		t.Fatalf("body = %q content-type=%q", body.bytes, body.contentType)
	}
}

func TestHTTPRequestRejectsImplicitGETBodyAsInvalidArguments(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	_, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url": "https://example.test", "body_text": "payload",
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("error = %v, want TOOL_ARGS_INVALID", err)
	}
}

func TestHTTPRequestRejectsInvalidHeaderAsInvalidArguments(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	_, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url": "https://example.test", "headers": []any{map[string]any{"name": "Bad Header", "value": "value"}},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("error = %v, want TOOL_ARGS_INVALID", err)
	}
}

func TestHTTPRequestRequiresExactLoopbackPorts(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	_, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url": "http://127.0.0.1:8080/health",
		"capability_request": map[string]any{
			"loopback_connect": map[string]any{},
		},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("error = %v, want TOOL_ARGS_INVALID", err)
	}
}

func TestTextualRejectsBinaryAndInvalidUTF8(t *testing.T) {
	if textual("application/octet-stream", []byte("text")) {
		t.Fatal("octet-stream was rendered as text")
	}
	if textual("text/plain", []byte{0xff}) {
		t.Fatal("invalid UTF-8 text was rendered")
	}
	if !textual("application/json; charset=utf-8", []byte(`{"ok":true}`)) {
		t.Fatal("JSON response was not rendered")
	}
}

func TestHTTPRequestDiscardDoesNotBufferResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(make([]byte, outboundhttp.DefaultBodyMax+1))
	}))
	t.Cleanup(server.Close)

	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	out, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url": server.URL, "response_body": "discard",
		"capability_request": loopbackCapability(t, server.URL),
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	})
	if err != nil {
		t.Fatalf("discard http_request: %v", err)
	}
	var got result
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode discarded response: %v", err)
	}
	if !got.BodyOmitted || got.Body != "" || got.Bytes != 0 || got.SHA256 != "" {
		t.Fatalf("discarded response = %+v", got)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatalf("decode discarded fields: %v", err)
	}
	if _, present := fields["bytes"]; present {
		t.Fatalf("discarded response claimed an observed byte count: %s", out)
	}
	if _, present := fields["sha256"]; present {
		t.Fatalf("discarded response claimed an observed hash: %s", out)
	}
}

type stubDirectIPRuntime struct {
	consumed int
}

func (*stubDirectIPRuntime) IssuePermit(hitl.EffectPermitIdentity, string, string, string) {}
func (*stubDirectIPRuntime) LeaseCovers(string, hitl.DirectIPLease) bool                  { return false }
func (*stubDirectIPRuntime) GrantChat(string, hitl.DirectIPLease, string, string, *time.Time) bool {
	return true
}
func (s *stubDirectIPRuntime) ConsumePermit(hitl.EffectPermitIdentity, string, string, string) (bool, error) {
	s.consumed++
	return s.consumed == 1, nil
}
func (s *stubDirectIPRuntime) Authorized(hitl.EffectPermitIdentity, string) bool {
	return s.consumed == 0
}

func TestDirectIPPolicyAllowsPrivateUnicastAndRespectsDeclaredDestinations(t *testing.T) {
	req, reject := capabilityrequest.ParseCapabilityRequest(map[string]any{
		"capability_request": map[string]any{
			"direct_ip": map[string]any{
				"declared_destinations": []any{"192.0.2.1:8080"},
			},
		},
	})
	if reject != nil {
		t.Fatalf("ParseCapabilityRequest: %v", reject)
	}
	allow, reject := loopbackPolicy(req, tools.ToolContext{})
	if reject != nil || allow == nil {
		t.Fatalf("loopbackPolicy allow=%t, reject=%v", allow != nil, reject)
	}
	if !allow(netip.MustParseAddr("192.0.2.1"), 8080) {
		t.Fatal("declared private destination 192.0.2.1:8080 was denied")
	}
	if allow(netip.MustParseAddr("192.0.2.1"), 9090) {
		t.Fatal("undeclared port 192.0.2.1:9090 was allowed")
	}
	if allow(netip.MustParseAddr("192.0.2.2"), 8080) {
		t.Fatal("undeclared address 192.0.2.2:8080 was allowed")
	}
	if allow(netip.MustParseAddr("169.254.169.254"), 8080) {
		t.Fatal("link-local metadata address was allowed under direct_ip")
	}
	if allow(netip.MustParseAddr("127.0.0.1"), 8080) {
		t.Fatal("loopback address was allowed under direct_ip without loopback_connect")
	}
}

func TestHTTPRequestDirectIPConsumesPermitAndRejectsReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	rt := &stubDirectIPRuntime{}
	var phases []tools.DirectIPLifecyclePhase
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID, SessionID: "s1", ToolCallID: "c1"},
		Local:    tools.InvocationLocal{LoopbackConnectGranted: true},
		Direct: tools.InvocationDirect{
			DirectIPRequested:         true,
			DirectIPAuthorized:        true,
			DirectIPDeclared:          []string{"192.0.2.1:8080"},
			DirectIPActionDigest:      "act",
			DirectIPRequestDigest:     "req",
			DirectIPConfineDigest:     "conf",
			DirectIPCapabilityRuntime: rt,
			DirectIPLifecycle: func(ev tools.DirectIPLifecycleEvent) {
				phases = append(phases, ev.Phase)
			},
		},
	}
	args := map[string]any{
		"url": server.URL,
		"capability_request": map[string]any{
			"direct_ip": map[string]any{"declared_destinations": []any{"192.0.2.1:8080"}},
		},
	}
	if _, err := registry.Run(t.Context(), "http_request", args, tctx); err != nil {
		t.Fatalf("first direct_ip http_request failed: %v", err)
	}
	if rt.consumed != 1 || len(phases) != 2 || phases[0] != tools.DirectIPLifecycleStarted || phases[1] != tools.DirectIPLifecycleCompleted {
		t.Fatalf("consumed=%d phases=%v", rt.consumed, phases)
	}
	_, err := registry.Run(t.Context(), "http_request", args, tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != isolation.CodeDirectIPAuthorizationChanged {
		t.Fatalf("second run error = %v, want %s", err, isolation.CodeDirectIPAuthorizationChanged)
	}
}
