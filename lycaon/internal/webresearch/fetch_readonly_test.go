package webresearch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

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

func TestMapFetchToolErrOversizeIsStructured(t *testing.T) {
	err := mapFetchToolErr(FetchBodyTooLargeError{Limit: fetchBodyByteLimit, Actual: fetchBodyByteLimit + 1})
	reject := &toolrejection.ToolReject{}
	if !errors.As(err, &reject) || reject.Code != "FETCH_URL_BODY_TOO_LARGE" {
		t.Fatalf("error = %v want FETCH_URL_BODY_TOO_LARGE", err)
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
	reject := &toolrejection.ToolReject{}
	ok := errors.As(err, &reject)
	if !ok || reject.Code != "FETCH_URL_BLOCKED" {
		t.Fatalf("want FETCH_URL_BLOCKED, got %T %v", err, err)
	}
}

func TestFetchURLRejectsNonResearchBodies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)
	for _, contentType := range []string{"font/woff2", "application/zip", "video/mp4", "application/octet-stream"} {
		t.Run(contentType, func(t *testing.T) {
			url := testHTTPServer(t, contentType, "binary\x00body")
			_, _, err := fetchURLTool(t.Context(), fetchURLToolArgs{URL: url})
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != "FETCH_URL_TYPE_UNSUPPORTED" {
				t.Fatalf("reject = %v", err)
			}
		})
	}
}

func TestFetchURLRejectsOversizeBeforePublishing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	allowLoopbackFetch(t)
	url := testHTTPServer(t, "text/plain", strings.Repeat("x", fetchBodyByteLimit+1))
	out, fetched, err := fetchURLTool(t.Context(), fetchURLToolArgs{URL: url})
	var tooLarge FetchBodyTooLargeError
	if !errors.As(err, &tooLarge) || out != "" || fetched != nil {
		t.Fatalf("out=%q fetched=%v err=%v", out, fetched, err)
	}
}

func TestFetchURLRejectsCachedNonDocument(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	url := "https://example.org/asset"
	testutil.FailErr(t, "cache asset", tooloutput.WriteFetchCache(url, tooloutput.FetchCacheEntry{URL: url, ContentType: "font/woff2", Body: "cached font", Ext: "woff2"}))
	out, fetched, err := fetchURLTool(t.Context(), fetchURLToolArgs{URL: url})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "FETCH_URL_TYPE_UNSUPPORTED" || out != "" || fetched != nil {
		t.Fatalf("out=%q fetched=%v err=%v", out, fetched, err)
	}
}
