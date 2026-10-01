package webresearch

import (
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCollectProbeMetaUnescapesDescription(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<!doctype html><html><head>
<meta name="description" content="Steam &amp; Machines on sale">
<title>Page</title>
</head><body><p>body</p></body></html>`))
	testutil.FailErr(t, "parse", err)
	var probe pageProbe
	collectProbeMeta(doc, &probe)
	if probe.description != "Steam & Machines on sale" {
		t.Fatalf("description = %q", probe.description)
	}
	if strings.Contains(probe.description, "&amp;") {
		t.Fatalf("entity not unescaped: %q", probe.description)
	}
}

func TestProbeSnippetNormalizesNotesMarkup(t *testing.T) {
	scorer := newQueryScorer("steam machine", nil, false, CurrentPeriod())
	got := probeSnippet(indexCandidate{Title: "t", Notes: `<b>Note&amp;Body</b>`}, pageProbe{}, scorer)
	if got != "Note&Body" {
		t.Fatalf("snippet = %q", got)
	}
	if strings.Contains(got, "<") {
		t.Fatalf("snippet still has markup: %q", got)
	}
}

func TestExtractHTMLTextFailureReturnsStubNotRawHTML(t *testing.T) {
	raw := `<html><head><script>var x="<html>payload</html>"</script></head><body></body></html>`
	_, text := extractHTMLText(raw, "https://example.com/empty")
	if text != fetchHTMLExtractStub {
		t.Fatalf("expected stub, got %q", text)
	}
	if strings.Contains(text, "<html>") || strings.Contains(text, "payload") {
		t.Fatalf("stub leaked raw markup: %q", text)
	}
}
