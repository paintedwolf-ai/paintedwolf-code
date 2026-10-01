package visualscreen

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func svgDrawing(href string) []byte {
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="40" height="40">` +
		`<text>Header</text><image xlink:href="` + href + `" width="40" height="40"/></svg>`)
}

func pngDataURI(t *testing.T) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(whitePNG(t, 4, 4))
}

func TestSVGEmbeddedRasterTextIsScreened(t *testing.T) {
	ocr := &fakeOCR{spans: []TextSpan{{Text: "key " + testSecret}}}
	scanned, err := NewScanner(ocr).Scan(context.Background(), "image/svg+xml", svgDrawing(pngDataURI(t)))
	testutil.FailErr(t, "scan", err)
	if ocr.calls != 1 || !strings.Contains(scanned.ExtractedText, testSecret) || scanned.Gap != "" {
		t.Fatalf("embedded raster not read: calls=%d scanned=%+v", ocr.calls, scanned)
	}

	styled := []byte(`<svg width="10" height="10"><rect style="fill: url('` + pngDataURI(t) + `')"/></svg>`)
	ocr = &fakeOCR{spans: []TextSpan{{Text: "styled"}}}
	scanned, err = NewScanner(ocr).Scan(context.Background(), "image/svg+xml", styled)
	testutil.FailErr(t, "scan styled", err)
	if ocr.calls != 1 || !strings.Contains(scanned.ExtractedText, "styled") {
		t.Fatalf("css-embedded raster not read: calls=%d scanned=%+v", ocr.calls, scanned)
	}
}

func TestSVGEmbeddedRasterFollowsOCRCoverage(t *testing.T) {
	scanned, err := NewScanner(&fakeOCR{err: ErrOCRUnavailable}).Scan(context.Background(), "image/svg+xml", svgDrawing(pngDataURI(t)))
	testutil.FailErr(t, "scan", err)
	if scanned.Gap != secretmatch.GapOCRUnavailable {
		t.Fatalf("gap = %q, want ocr_unavailable for an embedded raster", scanned.Gap)
	}
	undecodable := "data:image/avif;base64," + base64.StdEncoding.EncodeToString([]byte("not a raster the screen decodes"))
	scanned, err = NewScanner(&fakeOCR{}).Scan(context.Background(), "image/svg+xml", svgDrawing(undecodable))
	testutil.FailErr(t, "scan undecodable", err)
	if scanned.Gap != secretmatch.GapEmbeddedReference {
		t.Fatalf("gap = %q, want embedded_reference for an image the screen cannot decode", scanned.Gap)
	}
}

func TestSVGExternalReferenceIsUnscreenedOnlyWhenTheRendererLoadsIt(t *testing.T) {
	const asset, remote = "http://asset.example/logo.png", "https://cdn.example/logo.png"
	loads := func(ref string) bool { return ref == asset }
	for _, tc := range []struct {
		name    string
		scanner *Scanner
		href    string
		wantGap bool
	}{
		{"loaded project asset", NewScanner(&fakeOCR{}).WithRenderedReferences(loads), asset, true},
		{"blocked remote", NewScanner(&fakeOCR{}).WithRenderedReferences(loads), remote, false},
		{"fragment", NewScanner(&fakeOCR{}).WithRenderedReferences(loads), "#glyph", false},
		{"undeclared renderer", NewScanner(&fakeOCR{}), remote, true},
	} {
		scanned, err := tc.scanner.Scan(context.Background(), "image/svg+xml", svgDrawing(tc.href))
		testutil.FailErr(t, tc.name, err)
		if got := scanned.Gap == secretmatch.GapEmbeddedReference; got != tc.wantGap {
			t.Errorf("%s: gap = %q, want unscreened=%v", tc.name, scanned.Gap, tc.wantGap)
		}
	}
}

func TestGateWithholdsSVGWhenEmbeddedRasterMatchCannotBeRedacted(t *testing.T) {
	g := NewGate(NewScanner(&fakeOCR{spans: []TextSpan{{Text: testSecret}}}), bundledMatcher(t), decide(secretmatch.SendRedacted, nil))
	outcome, err := g.Screen(context.Background(), VisualScreenInput{Mime: "image/svg+xml", RawBytes: svgDrawing(pngDataURI(t))})
	testutil.FailErr(t, "screen", err)
	if outcome.PermitPerception() || outcome.Perception != PerceptionWithheld || strings.Contains(outcome.ExtractedText, testSecret) {
		t.Fatalf("outcome = %+v, want withheld pixels and redacted text", outcome)
	}
}
