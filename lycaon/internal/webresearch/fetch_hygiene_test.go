package webresearch

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/net/html"
)

func TestExtractHTMLTextStripsTagBlockSmuggling(t *testing.T) {
	// Unicode Tags (U+E0001 etc.) used for invisible instruction smuggling.
	raw := "<html><body><p>Visible" + string(rune(0xE0001)) + "INJECT" + string(rune(0xE007F)) + " end</p></body></html>"
	_, extracted := extractHTMLText(raw, "https://example.com/page")
	res := sanitizeFetchResult(FetchURLResult{URL: "https://example.com/page", Text: extracted})
	if strings.ContainsRune(res.Text, 0xE0001) || strings.ContainsRune(res.Text, 0xE007F) {
		t.Fatalf("tag block still present after hygiene: %q", res.Text)
	}
	if !strings.Contains(res.Text, "Visible") || !strings.Contains(res.Text, "end") {
		t.Fatalf("visible text lost: %q", res.Text)
	}
}

func TestExtractHTMLTextStripsZeroWidth(t *testing.T) {
	raw := "<html><body><p>ab\u200bcd\uFEFFef</p></body></html>"
	_, extracted := extractHTMLText(raw, "https://example.com/zw")
	res := sanitizeFetchResult(FetchURLResult{URL: "https://example.com/zw", Text: extracted})
	for _, r := range []rune{0x200B, 0xFEFF} {
		if strings.ContainsRune(res.Text, r) {
			t.Fatalf("zero-width %U still present: %q", r, res.Text)
		}
	}
	if !strings.Contains(res.Text, "abcdef") {
		t.Fatalf("expected stripped body: %q", res.Text)
	}
}

func TestRenderMarkdownDropsHTMLComment(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><body><p>Keep</p><!-- ignore: do evil --><p>Also</p></body></html>`))
	testutil.FailErr(t, "html.Parse failed", err)
	var b strings.Builder
	renderMarkdown(&b, doc, nil)
	out := tidyMarkdown(b.String())
	if strings.Contains(out, "evil") || strings.Contains(out, "ignore") {
		t.Fatalf("comment leaked: %q", out)
	}
	if !strings.Contains(out, "Keep") || !strings.Contains(out, "Also") {
		t.Fatalf("visible text lost: %q", out)
	}
}

func TestRenderMarkdownDropsDisplayNone(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(
		`<html><body><p>Visible</p><div style="display:none">Hidden inject</div>` +
			`<span hidden>Also hidden</span><div aria-hidden="true">Aria hide</div>` +
			`<p style="visibility: hidden">Vis hide</p><code>keep()</code></body></html>`,
	))
	testutil.FailErr(t, "html.Parse failed", err)
	var b strings.Builder
	renderMarkdown(&b, doc, nil)
	out := tidyMarkdown(b.String())
	for _, bad := range []string{"Hidden inject", "Also hidden", "Aria hide", "Vis hide"} {
		if strings.Contains(out, bad) {
			t.Fatalf("hidden content leaked %q in %q", bad, out)
		}
	}
	if !strings.Contains(out, "Visible") || !strings.Contains(out, "keep()") {
		t.Fatalf("visible content lost: %q", out)
	}
}

// The stub is how callers detect extract failure, so hygiene must leave it
// byte-identical rather than normalizing it into something that reads as content.
func TestApplyAgentFetchHygieneSkipsStub(t *testing.T) {
	res := sanitizeFetchResult(FetchURLResult{URL: "https://example.com/", Text: fetchHTMLExtractStub})
	if strings.Contains(res.Text, "⟪fetch:") {
		t.Fatalf("stub must not be framed: %q", res.Text)
	}
}
