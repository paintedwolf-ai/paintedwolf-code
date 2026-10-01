package httpaction

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
)

func responsePathContext(root string) tools.ToolContext {
	return tools.ToolContext{
		Agent: tools.DefaultToolProfileID,
		Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root",
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
		var reject *tools.ToolReject
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
		var reject *tools.ToolReject
		if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("args %v error = %v, want TOOL_ARGS_INVALID", args, err)
		}
	}
}
