package webresearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func rawTestBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	return sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{{
		ID:    tools.DefaultToolProfileID,
		Tools: map[string]bool{"fetch_url": true, "write": true},
	}})
}

func TestClassifyFetchMIME(t *testing.T) {
	cases := []struct {
		ct   string
		path string
		body string
		want mimeClass
	}{
		{"text/css", "/a.css", "body{}", mimeTextual},
		{"application/javascript", "/a.js", "x()", mimeTextual},
		{"image/svg+xml", "/a.svg", "<svg/>", mimeTextual},
		{"image/png", "/a.png", "\x89PNG", mimeBinary},
		{"font/woff2", "/a.woff2", "w2", mimeBinary},
		{"application/octet-stream", "/logo.png", "\x00\x01", mimeBinary},
		{"application/zip", "/a.zip", "PK", mimeUnsupported},
		{"video/mp4", "/a.mp4", "ftyp", mimeUnsupported},
	}
	for _, c := range cases {
		got, _ := classifyFetchMIME(c.ct, c.path, []byte(c.body))
		if got != c.want {
			t.Errorf("classify(%q,%q) = %s want %s", c.ct, c.path, got, c.want)
		}
	}
}

func TestFetchURLRawPreservesSVGAndStyle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)
	html := `<html><head><style>.x{color:red}</style></head><body><svg><circle/></svg><p>hi</p></body></html>`
	srv := testHTTPServer(t, "text/html", html)

	out, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv, Mode: "raw"})
	testutil.FailErr(t, "raw fetch", err)
	if !strings.Contains(out, "<style>") || !strings.Contains(out, "<svg>") {
		t.Fatalf("raw must keep style/svg:\n%s", firstLines(out, 20))
	}
	// Raw mode returns the page bytes unframed. Marking is applied once, at the
	// message seam, from provenance — a body this package returns, caches, or
	// pages must not carry a marker whose nonce would then be replayed.
	if strings.Contains(out, "⟪") {
		t.Fatalf("tool return value carries a retrieval marker:\n%s", firstLines(out, 20))
	}
}

func TestFetchURLRawBinaryRequiresDest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)
	font := []byte("wOFF2...dummyfontdata...")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "font/woff2")
		_, _ = w.Write(font)
	}))
	t.Cleanup(srv.Close)

	_, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv.URL + "/font.woff2", Mode: "raw"})
	if err == nil {
		t.Fatal("expected dest required")
	}
	reject := &tools.ToolReject{}
	ok := errors.As(err, &reject)
	if !ok || reject.Code != "FETCH_URL_DEST_REQUIRED" {
		t.Fatalf("got %v", err)
	}
}

func TestFetchURLRawBinaryWritesDest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)
	root := t.TempDir()
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	t.Cleanup(srv.Close)

	out, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{
		URL:      srv.URL + "/logo.png",
		Mode:     "raw",
		Dest:     "assets/logo.png",
		Boundary: rawTestBoundary(t),
		Tctx: tools.ToolContext{
			Roots:        []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}},
			ActiveRootID: "primary",
		},
	})
	testutil.FailErr(t, "raw write", err)
	var receipt fetchAssetReceipt
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatalf("receipt json: %v\n%s", err, out)
	}
	if !receipt.Written || receipt.Bytes != len(png) || receipt.Dest != "assets/logo.png" {
		t.Fatalf("bad receipt: %+v", receipt)
	}
	got, err := os.ReadFile(filepath.Join(root, "assets", "logo.png"))
	testutil.FailErr(t, "read written", err)
	if string(got) != string(png) {
		t.Fatalf("written bytes mismatch")
	}
}

func TestFetchURLRawOversizeDoesNotWritePartialDest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)
	root := t.TempDir()
	body := strings.Repeat("x", fetchBodyByteLimit+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	_, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{
		URL:      srv.URL + "/large.png",
		Mode:     "raw",
		Dest:     "assets/large.png",
		Boundary: rawTestBoundary(t),
		Tctx: tools.ToolContext{
			Roots:        []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}},
			ActiveRootID: "primary",
		},
	})
	var tooLarge FetchBodyTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error = %v want FetchBodyTooLargeError", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "assets", "large.png")); !os.IsNotExist(statErr) {
		t.Fatalf("partial destination exists: %v", statErr)
	}
}

func TestMapFetchToolErrOversizeIsStructured(t *testing.T) {
	err := mapFetchToolErr(FetchBodyTooLargeError{Limit: fetchBodyByteLimit, Actual: fetchBodyByteLimit + 1})
	reject := &tools.ToolReject{}
	if !errors.As(err, &reject) || reject.Code != "FETCH_URL_BODY_TOO_LARGE" {
		t.Fatalf("error = %v want FETCH_URL_BODY_TOO_LARGE", err)
	}
}

func TestFetchURLTextModeRejectsDest(t *testing.T) {
	_, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{
		URL:  "https://example.com/x",
		Mode: "text",
		Dest: "assets/x.css",
	})
	reject := &tools.ToolReject{}
	ok := errors.As(err, &reject)
	if !ok || reject.Code != "FETCH_URL_DEST_INVALID" {
		t.Fatalf("got %v", err)
	}
}

func TestFetchURLModeInvalid(t *testing.T) {
	_, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: "https://example.com", Mode: "html"})
	reject := &tools.ToolReject{}
	ok := errors.As(err, &reject)
	if !ok || reject.Code != "FETCH_URL_MODE_INVALID" {
		t.Fatalf("got %v", err)
	}
}

func TestFetchURLRawUnsupportedType(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)
	srv := testHTTPServer(t, "application/zip", "PK\x03\x04")
	_, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv + "/a.zip", Mode: "raw"})
	reject := &tools.ToolReject{}
	ok := errors.As(err, &reject)
	if !ok || reject.Code != "FETCH_URL_TYPE_UNSUPPORTED" {
		t.Fatalf("got %v", err)
	}
}

func TestFetchURLBlocksLoopbackMapsToRejectViaTool(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	if err := RegisterToolsWithFactory(reg, ToolDeps{}, nil); err != nil {
		testutil.FailErr(t, "RegisterToolsWithFactory failed", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("nope"))
	}))
	t.Cleanup(srv.Close)
	_, err := reg.Run(context.Background(), "fetch_url", map[string]any{"url": srv.URL}, tools.ToolContext{})
	if err == nil {
		t.Fatal("expected SSRF block")
	}
	reject := &tools.ToolReject{}
	ok := errors.As(err, &reject)
	if !ok || reject.Code != "FETCH_URL_BLOCKED" {
		t.Fatalf("want FETCH_URL_BLOCKED, got %T %v", err, err)
	}
}
