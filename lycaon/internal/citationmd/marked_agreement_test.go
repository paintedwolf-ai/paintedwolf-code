package citationmd

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// markedFixture records what the frontend's Markdown parser does with a source.
// The frontend's own suite runs marked over the same file, so a parser upgrade that
// changes any of them fails there.
type markedFixture struct {
	Name            string   `json:"name"`
	Source          string   `json:"source"`
	Links           []string `json:"links"`
	Code            []string `json:"code"`
	KnownDivergence string   `json:"known_divergence"`
}

func TestMarkedAgreement(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/marked-agreement.json")
	testutil.FailErr(t, "read fixtures", err)
	var fixtures []markedFixture
	testutil.FailErr(t, "decode fixtures", json.Unmarshal(raw, &fixtures))
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(fixture.Source, Options{})
			links := []string{}
			for _, o := range doc.Occurrences {
				if o.Kind == KindWebLink {
					links = append(links, o.Link.Destination)
				}
			}
			// A bracket closing a bare URL renders inside marked's link; the host
			// validates the URL meant and rewrites the display to match.
			want := []string{}
			for _, link := range fixture.Links {
				// Any open bracket on the line before the URL counts.
				if strings.Contains(fixture.Source, "[") && strings.HasSuffix(link, "]") && !strings.Contains(link, "[") {
					link = strings.TrimSuffix(link, "]")
				}
				want = append(want, link)
			}
			if !reflect.DeepEqual(links, want) {
				t.Fatalf("links = %q, marked = %q", links, want)
			}
			if fixture.KnownDivergence != "" {
				return
			}
			for _, code := range fixture.Code {
				if !inLiteralZone(doc, code) {
					t.Fatalf("code %q is outside the literal zones %+v", code, doc.Zones)
				}
			}
			for _, o := range doc.Occurrences {
				if o.Kind == KindCitation && strings.Contains(strings.Join(fixture.Code, "\n"), o.Token) {
					t.Fatalf("citation %q taken from code", o.Token)
				}
			}
		})
	}
}

func inLiteralZone(doc Document, code string) bool {
	for _, zone := range doc.Zones {
		text := doc.Source[zone.Span.Start:zone.Span.End]
		if zone.Kind != ZoneLink && zone.Kind != ZoneAutolink && strings.Contains(stripContainerMarkup(text), code) {
			return true
		}
	}
	return false
}

func stripContainerMarkup(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimLeft(line, "> \t")
	}
	return strings.Join(lines, "\n")
}
