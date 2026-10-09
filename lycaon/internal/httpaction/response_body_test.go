package httpaction

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func bodyServer(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSmallTextBodyStaysInline(t *testing.T) {
	server := bodyServer(t, "application/json", []byte(`{"ok":true}`))
	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL, "capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(t.TempDir(), "call-1"))
	testutil.FailErr(t, "inline body", err)
	if got.Body != `{"ok":true}` || got.BodyEncoding != "utf-8" || got.BodyOmitted || got.BodySpillPath != "" {
		t.Fatalf("result = %+v", got)
	}
}

func TestOversizeTextBodyLandsWholeUnderHostData(t *testing.T) {
	payload := strings.Repeat("abcdefgh", (maxInlineBody/8)+64)
	server := bodyServer(t, "application/json", []byte(payload))
	hostData := t.TempDir()
	tctx := sessionContext(t.TempDir(), "call-1")
	tctx.HostDataDir = hostData

	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL, "capability_request": loopbackCapability(t, server.URL),
	}, tctx)
	testutil.FailErr(t, "oversize body", err)
	if got.Body != "" || !got.BodyOmitted || got.BodyUnavailable {
		t.Fatalf("an oversize body was cut into the result: %+v", got)
	}
	if got.BodySpillPath == "" || !strings.HasPrefix(got.BodySpillPath, "tool-output/") {
		t.Fatalf("body_spill_path = %q, want a host-data tool-output path", got.BodySpillPath)
	}
	if got.Bytes != int64(len(payload)) {
		t.Fatalf("bytes = %d, want %d", got.Bytes, len(payload))
	}
	// The landed bytes are the ones the digest describes, whole and unmodified,
	// and they sit where projectpaths resolves a host-data read.
	scopeRel, ok := tooloutput.AgentWireSpillScopeRel(hostData, got.BodySpillPath)
	if !ok {
		t.Fatalf("body_spill_path %q does not resolve as a host-data read", got.BodySpillPath)
	}
	stored, readErr := os.ReadFile(tooloutput.DiskPath(hostData, scopeRel))
	testutil.FailErr(t, "read landed body", readErr)
	landed, decodeErr := zstdcodec.Decompress(stored)
	testutil.FailErr(t, "decompress landed body", decodeErr)
	if string(landed) != payload {
		t.Fatalf("landed %d bytes, want the %d that arrived", len(landed), len(payload))
	}
}

func TestOversizeBodyWithoutHostDataSaysItIsUnavailable(t *testing.T) {
	payload := strings.Repeat("x", maxInlineBody+1)
	server := bodyServer(t, "text/plain", []byte(payload))
	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL, "capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(t.TempDir(), "call-1"))
	testutil.FailErr(t, "oversize body", err)
	if !got.BodyUnavailable || got.Body != "" || got.BodySpillPath != "" {
		t.Fatalf("result = %+v, want an admitted unavailable body", got)
	}
	if got.SHA256 == "" || got.Bytes != int64(len(payload)) {
		t.Fatalf("an unavailable body dropped the facts it did observe: %+v", got)
	}
}

func TestBinaryBodyIsOmittedRatherThanLanded(t *testing.T) {
	server := bodyServer(t, "application/octet-stream", []byte{0x00, 0x01, 0x02})
	tctx := sessionContext(t.TempDir(), "call-1")
	tctx.HostDataDir = t.TempDir()
	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL, "capability_request": loopbackCapability(t, server.URL),
	}, tctx)
	testutil.FailErr(t, "binary body", err)
	if !got.BodyOmitted || got.BodyEncoding != "binary" || got.BodySpillPath != "" {
		t.Fatalf("result = %+v", got)
	}
}

func TestHeadResultClaimsNoBody(t *testing.T) {
	server := bodyServer(t, "application/json", nil)
	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL, "method": "HEAD", "capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(t.TempDir(), "call-1"))
	testutil.FailErr(t, "head request", err)
	if got.SHA256 != "" || got.Bytes != 0 || got.BodyEncoding != "" || got.Body != "" {
		t.Fatalf("HEAD claimed body facts: %+v", got)
	}
	if !got.BodyOmitted {
		t.Fatalf("HEAD did not admit the body is absent: %+v", got)
	}
}

func TestQueryAppendsInOrderAndLeavesTheURLQueryAlone(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RawQuery
	}))
	t.Cleanup(server.Close)
	_, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL + "/x?sig=abc&b=2&a=1&flag",
		"query": []any{
			map[string]any{"name": "z", "value": "1"},
			map[string]any{"name": "a", "value": "2"},
			map[string]any{"name": "m", "value": "3"},
		},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(t.TempDir(), "call-1"))
	testutil.FailErr(t, "query request", err)
	// A signed query keeps its bytes and its order; declared parameters follow
	// in the order they were declared.
	if seen != "sig=abc&b=2&a=1&flag&z=1&a=2&m=3" {
		t.Fatalf("query = %q", seen)
	}
}

func TestLiteralFormFieldsAreBoundedAsTheyAreBuilt(t *testing.T) {
	parts := make([]any, 0, 8)
	for i := 0; i < 8; i++ {
		parts = append(parts, map[string]any{"name": "f", "value": strings.Repeat("x", 5<<20)})
	}
	_, err := assembleBody(t.Context(), testBoundary(), tools.ToolContext{}, map[string]any{"form": parts})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want the request-body bound", err)
	}
}

func TestResponsePathStillLandsInTheProject(t *testing.T) {
	server := bodyServer(t, "application/json", []byte(`{"tags":[]}`))
	root := t.TempDir()
	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL, "response_path": "out/tags.json",
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "response_path", err)
	data, readErr := os.ReadFile(filepath.Join(root, "out", "tags.json"))
	testutil.FailErr(t, "read landed file", readErr)
	if string(data) != `{"tags":[]}` || !got.Written || got.ResponsePath != "out/tags.json" || got.BodySpillPath != "" {
		t.Fatalf("result = %+v file = %q", got, data)
	}
}

func TestPostThatEndsInSeeOtherLandsOnTheResource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/orders/7", http.StatusSeeOther)
	})
	mux.HandleFunc("/orders/7", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": server.URL + "/orders", "method": "POST", "body_json": map[string]any{"item": 1},
		"redirects": "safe", "capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(t.TempDir(), "call-1"))
	testutil.FailErr(t, "post with redirects", err)
	if got.Status != http.StatusOK || got.Body != `{"id":7}` {
		t.Fatalf("result = %+v, want the created resource rather than a bare 303", got)
	}
	if len(got.Redirects) != 1 || got.Redirects[0].Method != "GET" {
		t.Fatalf("redirects = %+v, want the rewritten hop", got.Redirects)
	}
}

func TestFailureRetryabilityComesFromTheFault(t *testing.T) {
	oversize := bodyServer(t, "application/json", make([]byte, (5<<20)+1))
	_, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": oversize.URL, "capability_request": loopbackCapability(t, oversize.URL),
	}, sessionContext(t.TempDir(), "call-1"))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "HTTP_REQUEST_FAILED" {
		t.Fatalf("error = %v", err)
	}
	if reject.Retryable {
		t.Fatal("a body past the bound was reported retryable; the retry fails identically")
	}
	if reject.Data["limit_bytes"] == nil {
		t.Fatalf("reject data = %v, want the bound that was crossed", reject.Data)
	}
}

func TestDeadlineFailureNamesTheDeadlineArgument(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(slow.Close)
	_, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": slow.URL, "timeout_ms": 1000, "capability_request": loopbackCapability(t, slow.URL),
	}, sessionContext(t.TempDir(), "call-1"))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("error = %v", err)
	}
	if !reject.Retryable {
		t.Fatal("a deadline was reported permanent")
	}
	if reject.Data["deadline"] != "timeout_ms" {
		t.Fatalf("reject data = %v, want the argument that bounds the exchange", reject.Data)
	}
}
