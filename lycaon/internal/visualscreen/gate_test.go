package visualscreen

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

const testSecret = "AKIAQYJK5TXV4NZR7SGB"

func bundledMatcher(t *testing.T) *secretmatch.Matcher {
	t.Helper()
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	if err != nil {
		testutil.FailErr(t, "build matcher", err)
	}
	return matcher
}

// fakeOCR returns fixed spans or a fixed error and counts calls.
type fakeOCR struct {
	spans []TextSpan
	err   error
	calls int
}

func (f *fakeOCR) RecognizeText(context.Context, string, []byte) ([]TextSpan, error) {
	f.calls++
	return f.spans, f.err
}

func decide(d secretmatch.Decision, seen *[]secretmatch.Alert) secretmatch.AskFunc {
	return func(_ context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
		if seen != nil {
			*seen = append(*seen, alert)
		}
		return secretmatch.Resolution{Decision: d}, nil
	}
}

func whitePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.White)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		testutil.FailErr(t, "encode png", err)
	}
	return buf.Bytes()
}

func TestGateScreensCleanSVG(t *testing.T) {
	g := NewGate(nil, bundledMatcher(t), func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		t.Fatal("unexpected secret screen ask for clean SVG")
		return secretmatch.Resolution{}, nil
	})
	outcome, err := g.Screen(context.Background(), VisualScreenInput{
		Mime:     "image/svg+xml",
		RawBytes: []byte(`<svg width="100" height="100"><text>Public Heading</text></svg>`),
	})
	if err != nil {
		testutil.FailErr(t, "screen", err)
	}
	if !outcome.PermitPerception() || !strings.Contains(outcome.ExtractedText, "Public Heading") {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGateSVGDecisions(t *testing.T) {
	matcher := bundledMatcher(t)
	svg := []byte(`<svg width="100" height="100"><title>deploy key</title><text x="1">key: ` + testSecret + `</text></svg>`)

	_, err := NewGate(nil, matcher, decide(secretmatch.Withhold, nil)).Screen(context.Background(), VisualScreenInput{Mime: "image/svg+xml", RawBytes: svg})
	if !errors.Is(err, ErrVisualSecretWithheld) {
		t.Fatalf("withhold err = %v", err)
	}

	var seen []secretmatch.Alert
	outcome, err := NewGate(nil, matcher, decide(secretmatch.SendUnchanged, &seen)).Screen(context.Background(), VisualScreenInput{Mime: "image/svg+xml", RawBytes: svg})
	if err != nil {
		testutil.FailErr(t, "send unchanged", err)
	}
	if outcome.Perception != PerceptionOriginal || !bytes.Equal(outcome.PerceiveBytes, svg) {
		t.Fatalf("unchanged outcome = %+v", outcome)
	}
	if len(seen) != 1 || seen[0].RuleID == secretmatch.UnscreenedRuleID || seen[0].ScreeningGap != "" || !seen[0].CanRedact() {
		t.Fatalf("match alert = %+v", seen)
	}
}

// Send redacted removes the matched run from the markup that is rasterized,
// not from a description of it.
func TestGateSVGRedactsMatchedMarkup(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><text aria-label="` + testSecret + `">key: ` + testSecret + `</text><!-- ` + testSecret + ` --></svg>`)
	outcome, err := NewGate(nil, bundledMatcher(t), decide(secretmatch.SendRedacted, nil)).Screen(context.Background(), VisualScreenInput{
		Mime: "image/svg+xml", RawBytes: svg,
	})
	if err != nil {
		testutil.FailErr(t, "screen", err)
	}
	if outcome.Perception != PerceptionRedacted {
		t.Fatalf("perception = %q", outcome.Perception)
	}
	if bytes.Contains(outcome.PerceiveBytes, []byte(testSecret)) {
		t.Fatalf("secret survives in perceived svg: %s", outcome.PerceiveBytes)
	}
	if strings.Contains(outcome.ExtractedText, testSecret) {
		t.Fatalf("secret survives in extracted text: %s", outcome.ExtractedText)
	}
	if err := xml.Unmarshal(outcome.PerceiveBytes, new(struct{})); err != nil {
		testutil.FailErr(t, "redacted svg parses", err)
	}
}

// A secret split across tspans matches as rendered, and send redacted removes
// each tspan's slice of it.
func TestGateSVGRedactsSecretSplitAcrossTspans(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><text x="1">key: <tspan>` + testSecret[:7] +
		`</tspan><tspan fill="red">` + testSecret[7:13] + `</tspan>
<tspan>` + testSecret[13:] + `</tspan></text></svg>`)
	var seen []secretmatch.Alert
	outcome, err := NewGate(nil, bundledMatcher(t), decide(secretmatch.SendRedacted, &seen)).Screen(context.Background(), VisualScreenInput{
		Mime: "image/svg+xml", RawBytes: svg,
	})
	if err != nil {
		testutil.FailErr(t, "screen", err)
	}
	if len(seen) != 1 || seen[0].RuleID == secretmatch.UnscreenedRuleID {
		t.Fatalf("split secret raised no match card: %+v", seen)
	}
	if outcome.Perception != PerceptionRedacted {
		t.Fatalf("perception = %q", outcome.Perception)
	}
	for _, part := range []string{testSecret[:7], testSecret[7:13], testSecret[13:]} {
		if bytes.Contains(outcome.PerceiveBytes, []byte(part)) {
			t.Fatalf("slice %q survives in perceived svg: %s", part, outcome.PerceiveBytes)
		}
	}
	if err := xml.Unmarshal(outcome.PerceiveBytes, new(struct{})); err != nil {
		testutil.FailErr(t, "redacted svg parses", err)
	}
}

// A split secret with an entity-encoded piece cannot be mapped, so the image
// is withheld.
func TestGateSVGSplitSecretWithUnmappablePieceWithholds(t *testing.T) {
	svg := []byte(`<svg><text><tspan>` + testSecret[:10] + `</tspan><tspan>&#` +
		fmt.Sprint(int(testSecret[10])) + `;` + testSecret[11:] + `</tspan></text></svg>`)
	outcome, err := NewGate(nil, bundledMatcher(t), decide(secretmatch.SendRedacted, nil)).Screen(context.Background(), VisualScreenInput{
		Mime: "image/svg+xml", RawBytes: svg,
	})
	if err != nil {
		testutil.FailErr(t, "screen", err)
	}
	if outcome.Perception != PerceptionWithheld || outcome.PerceiveBytes != nil {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// A match whose markup is entity-encoded has no verbatim byte range, so the
// image is withheld rather than claimed redacted.
func TestGateSVGUnmappableMatchWithholdsPixels(t *testing.T) {
	encoded := "&#65;" + testSecret[1:]
	svg := []byte(`<svg><text>` + encoded + `</text></svg>`)
	outcome, err := NewGate(nil, bundledMatcher(t), decide(secretmatch.SendRedacted, nil)).Screen(context.Background(), VisualScreenInput{
		Mime: "image/svg+xml", RawBytes: svg,
	})
	if err != nil {
		testutil.FailErr(t, "screen", err)
	}
	if outcome.Perception != PerceptionWithheld || outcome.PermitPerception() || outcome.PerceiveBytes != nil {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// Masking follows character offsets: only the span that produced the match
// is blacked out.
func TestGateRasterMasksOnlyMatchedSpan(t *testing.T) {
	raw := whitePNG(t, 100, 100)
	ocr := &fakeOCR{spans: []TextSpan{
		{Text: "Public heading", BoundingBox: [4]float64{0, 0, 0.5, 0.2}},
		{Text: "key " + testSecret, BoundingBox: [4]float64{0, 0.5, 0.5, 0.2}},
	}}
	outcome, err := NewGate(NewScanner(ocr), bundledMatcher(t), decide(secretmatch.SendRedacted, nil)).Screen(context.Background(), VisualScreenInput{
		Mime: "image/png", RawBytes: raw,
	})
	if err != nil {
		testutil.FailErr(t, "screen", err)
	}
	if outcome.Perception != PerceptionRedacted || outcome.Mime != "image/png" {
		t.Fatalf("outcome = %+v", outcome)
	}
	img, err := png.Decode(bytes.NewReader(outcome.PerceiveBytes))
	if err != nil {
		testutil.FailErr(t, "decode masked", err)
	}
	if r, _, _, _ := img.At(10, 10).RGBA(); r == 0 {
		t.Fatal("unmatched span was masked")
	}
	if r, _, _, _ := img.At(10, 60).RGBA(); r != 0 {
		t.Fatal("matched span was not masked")
	}
}

func TestGateRasterGapRaisesStructuredAsk(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want secretmatch.ScreeningGap
	}{
		{"unavailable", ErrOCRUnavailable, secretmatch.GapOCRUnavailable},
		{"failed", errors.New("vision request failed"), secretmatch.GapOCRFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []secretmatch.Alert
			g := NewGate(NewScanner(&fakeOCR{err: tc.err}), bundledMatcher(t), decide(secretmatch.SendUnchanged, &seen))
			outcome, err := g.Screen(context.Background(), VisualScreenInput{Mime: "image/png", RawBytes: whitePNG(t, 4, 4)})
			if err != nil {
				testutil.FailErr(t, "screen", err)
			}
			if len(seen) != 1 {
				t.Fatalf("asks = %d, want 1", len(seen))
			}
			alert := seen[0]
			if alert.ScreeningGap != tc.want || alert.RuleID != secretmatch.UnscreenedRuleID ||
				alert.Surface != secretmatch.SurfaceVisualModel || alert.CanRedact() {
				t.Fatalf("alert = %+v", alert)
			}
			if len(alert.Fingerprints) != 1 || alert.Fingerprints[0] != secretmatch.UnscreenedFingerprint {
				t.Fatalf("fingerprints = %v", alert.Fingerprints)
			}
			if outcome.Gap != tc.want || outcome.Perception != PerceptionOriginal {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

func TestGateGapWithRedactedDecisionWithholds(t *testing.T) {
	g := NewGate(NewScanner(&fakeOCR{err: ErrOCRUnavailable}), bundledMatcher(t), decide(secretmatch.SendRedacted, nil))
	outcome, err := g.Screen(context.Background(), VisualScreenInput{Mime: "image/png", RawBytes: whitePNG(t, 4, 4)})
	if err != nil {
		testutil.FailErr(t, "screen", err)
	}
	if outcome.Perception != PerceptionWithheld || outcome.PerceiveBytes != nil {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGateRejectsOversizedRasterBeforeOCR(t *testing.T) {
	ocr := &fakeOCR{}
	_, err := NewGate(NewScanner(ocr), bundledMatcher(t), decide(secretmatch.SendUnchanged, nil)).Screen(context.Background(), VisualScreenInput{
		Mime: "image/png", RawBytes: whitePNG(t, 4100, 1),
	})
	var dims *DimensionsError
	if !errors.As(err, &dims) || dims.Width != 4100 {
		t.Fatalf("err = %v", err)
	}
	if ocr.calls != 0 {
		t.Fatalf("OCR ran %d times before the bound check", ocr.calls)
	}
}
