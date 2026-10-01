package webresearch

import (
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/lycaon/lycaon/internal/testutil"
)

func headingMarkdown(t *testing.T, frag string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader("<html><body>" + frag + "</body></html>"))
	testutil.FailErr(t, "html.Parse failed", err)
	var b strings.Builder
	renderMarkdown(&b, doc, nil)
	return tidyMarkdown(b.String())
}

func TestHeadingStripsPermalinkAnchors(t *testing.T) {
	cases := map[string]string{
		"deno_jump":      `<h2>Permissions <a class="anchor" href="#permissions">Jump to heading</a></h2>`,
		"sphinx_pilcrow": `<h2>Config<a class="headerlink" href="#config">¶</a></h2>`,
		"docusaurus":     `<h3>Setup<a href="#setup" class="hash-link" aria-label="Direct link to Setup">` + "\u200b" + `</a></h3>`,
		"aria_only":      `<h2>Usage <a href="#usage" aria-label="Permalink to Usage">#</a></h2>`,
		"github_icon":    `<h3><a class="anchor" href="#cmd" aria-hidden="true"></a><code>/cmd</code></h3>`,
	}
	want := map[string]string{
		"deno_jump":      "## Permissions",
		"sphinx_pilcrow": "## Config",
		"docusaurus":     "### Setup",
		"aria_only":      "## Usage",
		"github_icon":    "### /cmd",
	}
	for name, frag := range cases {
		got := headingMarkdown(t, frag)
		if got != want[name] {
			t.Errorf("%s: got %q, want %q", name, got, want[name])
		}
		if strings.Contains(got, "Jump to heading") || strings.Contains(got, "¶") {
			t.Errorf("%s: affordance text leaked: %q", name, got)
		}
	}
}

func TestRealLinkInHeadingKept(t *testing.T) {
	// A genuine external link in a heading is content, not chrome — keep its text.
	got := headingMarkdown(t, `<h2>See <a href="https://go.dev/ref/spec">the spec</a></h2>`)
	if !strings.Contains(got, "the spec") {
		t.Fatalf("real link text dropped: %q", got)
	}
}
