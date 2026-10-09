package webresearch

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestFormatFetchResultInlineSmall(t *testing.T) {
	res := FetchURLResult{URL: "https://x/y", Status: 200, Text: "short body"}
	out := FormatFetchResult(res, fileoutline.Result{}, false)
	if !strings.Contains(out, "short body") {
		t.Fatalf("small body should inline verbatim: %s", out)
	}
	if strings.Contains(out, "map (") || strings.Contains(out, "offset") {
		t.Fatalf("small body should not produce a map/paging hint: %s", out)
	}
}

func TestFormatFetchResultAnnotatesThinHTML(t *testing.T) {
	// An HTML page reduced to footer-only text: the JS-shell case.
	res := FetchURLResult{
		URL: "https://x/app", Status: 200, Markdown: true,
		Title: "Steam Machine",
		Text:  "© Valve Corporation. All rights reserved.",
	}
	out := FormatFetchResult(res, fileoutline.Result{}, false)
	if !strings.Contains(out, fetchThinNote) {
		t.Fatalf("thin HTML page missing note: %s", out)
	}

	// Same size but plain text (not HTML-derived): a tiny real file, no note.
	res.Markdown = false
	out = FormatFetchResult(res, fileoutline.Result{}, false)
	if strings.Contains(out, fetchThinNote) {
		t.Fatalf("plain-text body must not carry the JS note: %s", out)
	}
}

func TestFormatFetchResultHeadMapLarge(t *testing.T) {
	var b strings.Builder
	b.WriteString("export function top() {}\n")
	for i := 0; i < 400; i++ {
		// Valid non-definition filler, long enough to push the body over the inline cap.
		b.WriteString("doThing(someArgument, anotherArgument, yetAnotherOne);\n")
	}
	b.WriteString("export function bottom() {}\n")
	body := b.String()
	res := FetchURLResult{URL: "https://x/script.js", Status: 200, ContentType: "text/plain", Text: body}
	outline := fileoutline.AnalyzeText(t.Context(), "script.js", []byte(body))
	out := FormatFetchResult(res, outline, false)

	if !strings.Contains(out, "fetch_url(url, offset, limit)") {
		t.Fatalf("missing paging instruction: %s", firstLines(out, 20))
	}
	if !strings.Contains(out, "function top") {
		t.Fatalf("map should surface top decl: %s", firstLines(out, 24))
	}
	if !strings.Contains(out, "head (L1–") {
		t.Fatalf("missing head section: %s", firstLines(out, 24))
	}
	// The mapped declaration lies beyond the inline window.
	if !strings.Contains(out, "function bottom") {
		t.Fatalf("map should reference bottom decl beyond the head: %s", out)
	}
}

func TestFormatFetchRangeReturnsVerbatimLines(t *testing.T) {
	body := "alpha\nbravo\ncharlie\ndelta\necho\n"
	out := FormatFetchRange("https://x/y", body, 2, 2)
	if !strings.Contains(out, "2|bravo") || !strings.Contains(out, "3|charlie") {
		t.Fatalf("range should return verbatim numbered lines: %q", out)
	}
	if strings.Contains(out, "alpha") || strings.Contains(out, "delta") {
		t.Fatalf("range must not leak lines outside [2,4): %q", out)
	}
}

func TestFetchURLToolCachesAndPages(t *testing.T) {
	testutil.SkipIfShort(t, "fetch URL tool with paging over httptest server")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	allowLoopbackFetch(t)

	var b strings.Builder
	b.WriteString("export function marker() {}\n")
	for i := 0; i < 500; i++ {
		b.WriteString("export const sym = () => 1;\n")
	}
	body := b.String()
	srv := testHTTPServer(t, "text/plain", body)

	out, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv})
	if err != nil {
		t.Fatalf("fetchURLTool: %v", err)
	}
	if !strings.Contains(out, "function marker()") {
		t.Fatalf("orientation should include index landmark: %s", firstLines(out, 12))
	}
	if cached, ok := tooloutput.ReadFetchCache(srv); !ok || cached.Body != body {
		t.Fatalf("body not cached globally: ok=%v", ok)
	}

	// Second orientation call is served from cache.
	out2, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv})
	if err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	if !strings.Contains(out2, "cache hit") {
		t.Fatalf("re-fetch should be a cache hit: %s", firstLines(out2, 4))
	}

	// Ranges page the cached body verbatim.
	out3, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv, Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("ranged fetch: %v", err)
	}
	if !strings.Contains(out3, "1|export function marker()") {
		t.Fatalf("ranged call should return verbatim line 1: %s", firstLines(out3, 4))
	}

	// `limit` alone means the same range from the top.
	out4, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv, Limit: 1})
	if err != nil {
		t.Fatalf("limit without offset: %v", err)
	}
	if out4 != out3 {
		t.Fatalf("limit alone should page from line 1: %s", firstLines(out4, 4))
	}

	// An open-ended range is what omitting both already asks for.
	if _, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv, Offset: 2}); err == nil {
		t.Fatal("offset without limit must still reject")
	}
}

func TestFetchURLToolCacheHitPreservesResponseMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	requestedURL := "https://original.example/start"
	entry := tooloutput.FetchCacheEntry{
		URL:         "https://final.example/not-found",
		Status:      404,
		ContentType: "text/html; charset=utf-8",
		Title:       "Missing page",
		Body:        "The requested page is gone.",
		Ext:         "md",
		Markdown:    true,
	}
	testutil.FailErr(t, "write fetch cache", tooloutput.WriteFetchCache(requestedURL, entry))

	invocation := &tools.ToolInvocationOut{}
	out, fetched, err := fetchURLTool(context.Background(), fetchURLToolArgs{
		URL:  requestedURL,
		Tctx: tools.ToolContext{Out: invocation},
	})
	testutil.FailErr(t, "cached fetch_url", err)
	if fetched != nil {
		t.Fatal("cache hit must not report a live fetch")
	}
	// Cached fetches retain the final response URL for retrieval attribution.
	if invocation.RetrievedFrom != "final.example" {
		t.Fatalf("cache hit reported provenance %q, want final.example", invocation.RetrievedFrom)
	}
	for _, want := range []string{
		"# Missing page",
		"status: 404 | text/html; charset=utf-8",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("cached output missing %q: %s", want, out)
		}
	}
}

func TestFetchURLToolRejectsIncompleteRange(t *testing.T) {
	_, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{
		URL: "https://example.com/docs", Offset: 2,
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("err = %#v want TOOL_ARGS_INVALID", err)
	}
}

func TestFetchURLToolMapsHTMLDocHeadings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	allowLoopbackFetch(t)

	// Navigation surrounds the article to exercise main-content extraction.
	var b strings.Builder
	b.WriteString("<html><head><title>Guide</title></head><body>")
	b.WriteString(`<nav><a href="/x">Home</a> <a href="/y">Sign in</a> <a href="/z">Search</a></nav>`)
	b.WriteString("<article><h1>Installation</h1><p>Intro paragraph with enough words to be real content.</p>")
	for i := 0; i < 300; i++ {
		b.WriteString("<p>Filler sentence number to grow the article well past the inline cap and look like prose.</p>")
	}
	b.WriteString("<h2>Configuration</h2><p>Config details here describing the setup at length.</p></article>")
	b.WriteString(`<footer>© 2026 Sign in Navigation</footer></body></html>`)
	srv := testHTTPServer(t, "text/html", b.String())

	out, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv})
	if err != nil {
		t.Fatalf("fetchURLTool: %v", err)
	}
	// The leading h1 may become the title; the subheading remains in the outline.
	if !strings.Contains(out, "Configuration") {
		t.Fatalf("article heading missing from map:\n%s", firstLines(out, 30))
	}
	if !strings.Contains(out, "map (") {
		t.Fatalf("expected a structural map for the doc page:\n%s", firstLines(out, 30))
	}
	// Page navigation is excluded from the heading map.
	if strings.Contains(out, "Sign in") {
		t.Fatalf("readability should strip chrome (Sign in) from the content:\n%s", firstLines(out, 40))
	}

	// Cached markdown retains its heading map.
	cached, ok := tooloutput.ReadFetchCache(srv)
	if !ok || cached.Ext != "md" {
		t.Fatalf("HTML doc should cache as markdown: cache=%#v ok=%v", cached, ok)
	}
	out2, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{URL: srv})
	if err != nil {
		t.Fatalf("cache-hit fetch: %v", err)
	}
	if !strings.Contains(out2, "cache hit") || !strings.Contains(out2, "Configuration") {
		t.Fatalf("cache hit should still map headings:\n%s", firstLines(out2, 30))
	}
}

func TestCanonicalFetchURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"https://github.com/OWASP/ASVS/blob/v5.0.0/5.0/en/0x20-V11-Cryptography.md",
			"https://raw.githubusercontent.com/OWASP/ASVS/v5.0.0/5.0/en/0x20-V11-Cryptography.md",
		},
		{
			"https://github.com/owner/repo/blob/main/README.md?plain=1",
			"https://raw.githubusercontent.com/owner/repo/main/README.md",
		},
		{
			"https://gitlab.com/group/sub/project/-/blob/main/docs/file.md",
			"https://gitlab.com/group/sub/project/-/raw/main/docs/file.md",
		},
		{
			"https://bitbucket.org/owner/repo/src/main/path/file.md",
			"https://bitbucket.org/owner/repo/raw/main/path/file.md",
		},
		{"https://gist.github.com/user/abc123", "https://gist.github.com/user/abc123/raw"},
		{"https://raw.githubusercontent.com/o/r/main/x.md", "https://raw.githubusercontent.com/o/r/main/x.md"},
		{"https://gist.github.com/user/abc123/raw", "https://gist.github.com/user/abc123/raw"},
		// Listing/tree pages, bare gist user, and other hosts — unchanged.
		{"https://github.com/OWASP/ASVS/tree/v5.0.0", "https://github.com/OWASP/ASVS/tree/v5.0.0"},
		{"https://gist.github.com/user", "https://gist.github.com/user"},
		{"https://go.dev/doc/security/best-practices", "https://go.dev/doc/security/best-practices"},
	}
	for _, c := range cases {
		if got := CanonicalFetchURL(c.in); got != c.want {
			t.Fatalf("CanonicalFetchURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
