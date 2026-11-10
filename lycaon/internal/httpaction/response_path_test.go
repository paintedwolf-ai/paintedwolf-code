package httpaction

import (
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
)

func responsePathContext(root string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "root"},
	}
}

func TestHTTPRequestResponsePathLandsBodyInProject(t *testing.T) {
	const payload = `{"tree":[{"path":"a"},{"path":"b"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	out, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url": server.URL + "/tree", "response_path": "tmp/tree.json",
		"capability_request": loopbackCapability(t, server.URL),
	}, responsePathContext(root))
	if err != nil {
		t.Fatalf("run http_request: %v", err)
	}
	var got result
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != http.StatusOK || !got.Written || got.ResponsePath != "tmp/tree.json" || !got.BodyOmitted || got.Body != "" {
		t.Fatalf("response = %+v, want a written receipt with no inline body", got)
	}
	landed, err := os.ReadFile(filepath.Join(root, "tmp", "tree.json"))
	if err != nil || string(landed) != payload {
		t.Fatalf("landed body = %q err=%v", landed, err)
	}
}

func TestHTTPRequestResponsePathRejectsProtectedDestinations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret=1"))
	}))
	t.Cleanup(server.Close)
	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	for _, dest := range []string{".env", "../outside.txt", ".git/config", "keys/server.pem"} {
		_, err := registry.Run(t.Context(), "http_request", map[string]any{
			"url": server.URL + "/", "response_path": dest,
			"capability_request": loopbackCapability(t, server.URL),
		}, responsePathContext(t.TempDir()))
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "HTTP_REQUEST_RESPONSE_PATH_DENIED" {
			t.Fatalf("response_path %q error = %v, want HTTP_REQUEST_RESPONSE_PATH_DENIED", dest, err)
		}
		if reject.Data["http_requests_started"] != 1 || reject.Data["http_responses_received"] != 1 || reject.Data["http_last_status"] != http.StatusOK {
			t.Fatalf("storage rejection hid exchange outcome: %#v", reject.Data)
		}
	}
}

func TestHTTPRequestResponsePathExcludesDiscard(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}
	for _, args := range []map[string]any{
		{"url": "https://example.test", "response_path": "out.json", "response_body": "discard"},
		{"url": "https://example.test", "response_path": "  "},
	} {
		_, err := registry.Run(t.Context(), "http_request", args, responsePathContext(t.TempDir()))
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("args %v error = %v, want TOOL_ARGS_INVALID", args, err)
		}
	}
}

func TestHTTPRequestReportsExchangeProvenanceForExternalAndLocalRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	port := parsed.Port()
	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	externalURL := "http://api.external.example:" + port + "/v1/feed"
	resolveEntry := map[string]any{"host": "api.external.example", "port": portNum, "ip": "127.0.0.1"}

	root := t.TempDir()
	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}

	var externalObs []ExchangeProvenance
	extCtx := WithExchangeProvenanceObserver(t.Context(), func(p ExchangeProvenance) {
		externalObs = append(externalObs, p)
	})
	if _, err := registry.Run(extCtx, "http_request", map[string]any{
		"url":                externalURL,
		"resolve":            []any{resolveEntry},
		"response_path":      "tmp/external.json",
		"capability_request": loopbackCapability(t, server.URL),
	}, responsePathContext(root)); err != nil {
		t.Fatalf("run external http_request: %v", err)
	}
	if len(externalObs) != 1 || !externalObs[0].External || !slices.Contains(externalObs[0].WrittenFiles, "tmp/external.json") || !slices.Contains(externalObs[0].ObservedURLs, externalURL) {
		t.Fatalf("external provenance = %+v, want External=true WrittenFiles=[tmp/external.json] ObservedURLs containing %s", externalObs, externalURL)
	}

	var localObs []ExchangeProvenance
	localCtx := WithExchangeProvenanceObserver(t.Context(), func(p ExchangeProvenance) {
		localObs = append(localObs, p)
	})
	if _, err := registry.Run(localCtx, "http_request", map[string]any{
		"url":                server.URL + "/local",
		"capability_request": loopbackCapability(t, server.URL),
	}, responsePathContext(root)); err != nil {
		t.Fatalf("run loopback http_request: %v", err)
	}
	if len(localObs) != 1 || localObs[0].External {
		t.Fatalf("loopback provenance = %+v, want External=false", localObs)
	}
}
