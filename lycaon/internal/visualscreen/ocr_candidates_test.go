package visualscreen

import (
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visualscreen/ocrfixture"
)

func TestParseTesseractTSVPopulatesWordCandidates(t *testing.T) {
	const tsv = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"5\t1\t1\t1\t1\t1\t10\t20\t40\t12\t95.0\texport\n" +
		"5\t1\t1\t1\t1\t2\t60\t20\t120\t12\t62.0\tTOKEN=blurred\n" +
		"5\t1\t1\t1\t1\t2\t60\t20\t120\t12\t58.0\tTOKEN=AKIAQ9Z2M4N8P3R6T5V7\n" +
		"5\t1\t1\t1\t1\t2\t60\t20\t120\t12\t55.0\tTOKEN=AK1AQ9Z2M4N8P3R6T5V7\n"
	spans, err := parseTesseractTSV([]byte(tsv), 200, 100)
	testutil.FailErr(t, "parseTesseractTSV", err)
	if len(spans) != 1 {
		t.Fatalf("len(spans) = %d, want 1", len(spans))
	}
	if spans[0].Text != "export TOKEN=blurred" {
		t.Fatalf("Text = %q, want %q", spans[0].Text, "export TOKEN=blurred")
	}
	wantCandidates := []string{
		"export TOKEN=AKIAQ9Z2M4N8P3R6T5V7",
		"export TOKEN=AK1AQ9Z2M4N8P3R6T5V7",
	}
	if len(spans[0].Candidates) != len(wantCandidates) {
		t.Fatalf("Candidates = %#v, want %#v", spans[0].Candidates, wantCandidates)
	}
	for i, want := range wantCandidates {
		if spans[0].Candidates[i] != want {
			t.Fatalf("Candidates[%d] = %q, want %q", i, spans[0].Candidates[i], want)
		}
	}
}

func TestGateScreensOCRCandidatesAndConfusionClassesWithBoundingBoxRedaction(t *testing.T) {
	matcher := bundledMatcher(t)
	raw, lines := renderPNG(t, ocrfixture.Options{
		Lines: []string{"Public heading", "credential line"},
	})

	cases := []struct {
		name string
		span TextSpan
	}{
		{
			name: "primary text with OCR confusions in prefix and trailing slash",
			span: TextSpan{
				Text:        "export AWS_KEY=AK1AQ9Z2M4N8P3R6T5V/",
				Confidence:  0.9,
				BoundingBox: lines[1].BoundingBox,
			},
		},
		{
			name: "non-secret primary text rescued by alternative OCR candidate",
			span: TextSpan{
				Text:        "export AWS_KEY=unrecognized_blur",
				Candidates:  []string{"export AWS_KEY=AKlAQ9Z2M4N8P3R6T5V/"},
				Confidence:  0.7,
				BoundingBox: lines[1].BoundingBox,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeOCR{
				spans: []TextSpan{
					{Text: "Public heading", Confidence: 0.99, BoundingBox: lines[0].BoundingBox},
					tc.span,
				},
			}
			var alerts []secretmatch.Alert
			gate := NewGate(NewScanner(engine), matcher, decide(secretmatch.SendRedacted, &alerts))
			outcome, err := gate.Screen(context.Background(), VisualScreenInput{
				Mime:     "image/png",
				RawBytes: raw,
			})
			testutil.FailErr(t, "gate.Screen", err)
			if len(alerts) != 1 || alerts[0].Occurrences != 1 {
				t.Fatalf("alerts = %+v, want 1 alert with 1 occurrence", alerts)
			}
			if outcome.Perception != PerceptionRedacted {
				t.Fatalf("Perception = %q, want %q", outcome.Perception, PerceptionRedacted)
			}
			if !strings.Contains(outcome.ExtractedText, secretmatch.RedactedMarker) ||
				strings.Contains(outcome.ExtractedText, "Q9Z2M4N8P3R6T5V") {
				t.Fatalf("ExtractedText = %q, expected redacted marker", outcome.ExtractedText)
			}
			masked, err := png.Decode(bytes.NewReader(outcome.PerceiveBytes))
			testutil.FailErr(t, "decode masked png", err)
			b := masked.Bounds()
			probe := func(box [4]float64) uint32 {
				x := b.Min.X + int((box[0]+box[2]/2)*float64(b.Dx()))
				y := b.Min.Y + int((box[1]+box[3]/2)*float64(b.Dy()))
				r, _, _, _ := masked.At(x, y).RGBA()
				return r
			}
			if probe(lines[1].BoundingBox) != 0 {
				t.Fatal("secret line bounding box was not masked")
			}
			if probe(lines[0].BoundingBox) == 0 {
				t.Fatal("non-secret heading bounding box was unexpectedly masked")
			}
		})
	}
}

func TestGateProvenanceKeepsNonOCRMarkupAndMetadataStrict(t *testing.T) {
	matcher := bundledMatcher(t)
	// Confused token has '1' in AK1A and '/' at the end, so strict matching
	// rejects it while OCR-provenance matching accepts it.
	const confusedToken = "AK1AQ9Z2M4N8P3R6T5V/"

	svgMarkup := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="200" height="60"><text x="10" y="30">` + confusedToken + `</text></svg>`)
	var svgAlerts []secretmatch.Alert
	svgGate := NewGate(NewScanner(&fakeOCR{}), matcher, decide(secretmatch.SendRedacted, &svgAlerts))
	svgOut, err := svgGate.Screen(context.Background(), VisualScreenInput{
		Mime:     "image/svg+xml",
		RawBytes: svgMarkup,
	})
	testutil.FailErr(t, "screen svg markup", err)
	if len(svgAlerts) != 0 || svgOut.Perception != PerceptionOriginal {
		t.Fatalf("non-OCR SVG markup triggered OCR confusion match: alerts=%+v outcome=%+v", svgAlerts, svgOut)
	}

	pngWithMeta := pngWithChunk(t, "tEXt", []byte("Comment\x00"+confusedToken))
	var metaAlerts []secretmatch.Alert
	metaGate := NewGate(NewScanner(&fakeOCR{}), matcher, decide(secretmatch.SendRedacted, &metaAlerts))
	metaOut, err := metaGate.Screen(context.Background(), VisualScreenInput{
		Mime:     "image/png",
		RawBytes: pngWithMeta,
	})
	testutil.FailErr(t, "screen png metadata", err)
	if len(metaAlerts) != 0 || metaOut.Perception != PerceptionOriginal {
		t.Fatalf("non-OCR PNG metadata triggered OCR confusion match: alerts=%+v outcome=%+v", metaAlerts, metaOut)
	}
}

// twentyFourLiveOCRSecrets is a 24-secret corpus (12 AKIA + 12 ghp_) rich in
// the confusable glyphs 0, O, 1, I, l, 5, S, and 7 rendered through the live
// platform OCR engine at multiple point sizes.
var twentyFourLiveOCRSecrets = []string{
	"AKIAIOSFODNN7EXAMPL3",
	"AKIAO2I3S57O2I3S57O2",
	"AKIA7S5I3O27S5I3O27S",
	"AKIAS5O7I2S5O7I2S5O7",
	"AKIAI7O5S2I7O5S2I7O5",
	"AKIAO7S5I2O7S5I2O7S5",
	"AKIA2O3I4S576O7I2S37",
	"AKIA5S7O2I35S7O2I35S",
	"AKIA7I2O5S37I2O5S37I",
	"AKIAO5I7S2O5I7S2O5I7",
	"AKIAS7O2I5S7O2I5S7O2",
	"AKIAI2S5O7I2S5O7I2S5",
	"ghp_0O1Il5S70O1Il5S70O1Il5S70O1Il5S7aBcD",
	"ghp_7S5lI1O07S5lI1O07S5lI1O07S5lI1O0xYzW",
	"ghp_I1l0O5S7I1l0O5S7I1l0O5S7I1l0O5S7mNkP",
	"ghp_5S0O71Il5S0O71Il5S0O71Il5S0O71IlqRtV",
	"ghp_O0l1I7S5O0l1I7S5O0l1I7S5O0l1I7S5bDfH",
	"ghp_l1I0O75Sl1I0O75Sl1I0O75Sl1I0O75SjKmN",
	"ghp_70O1Il5S70O1Il5S70O1Il5S70O1Il5SpQrT",
	"ghp_S570O1IlS570O1IlS570O1IlS570O1IlvWxZ",
	"ghp_1Il5S70O1Il5S70O1Il5S70O1Il5S70OcDeF",
	"ghp_0O5S71Il0O5S71Il0O5S71Il0O5S71IlgHiJ",
	"ghp_71Il0O5S71Il0O5S71Il0O5S71Il0O5SkLmP",
	"ghp_5S1Il0O75S1Il0O75S1Il0O75S1Il0O7rStU",
}

func TestPlatformEngineOCRConfusionSecretCorpusAcrossFontSizes(t *testing.T) {
	engine := platformEngine(t)
	matcher := bundledMatcher(t)
	pointSizes := []float64{18, 28, 40}

	totalRuns, totalExact, totalDetected := 0, 0, 0
	for _, pt := range pointSizes {
		exactAtSize, detectedAtSize := 0, 0
		for _, secret := range twentyFourLiveOCRSecrets {
			raw, _ := renderPNG(t, ocrfixture.Options{
				Lines:     []string{"export SECRET=" + secret},
				PointSize: pt,
			})
			scanned, err := NewScanner(engine).Scan(context.Background(), "image/png", raw)
			testutil.FailErr(t, "Scan", err)
			if strings.Contains(scanned.ExtractedText, secret) && len(matcher.Screen(scanned.ExtractedText)) > 0 {
				exactAtSize++
			}
			var alerts []secretmatch.Alert
			gate := NewGate(NewScanner(engine), matcher, decide(secretmatch.SendRedacted, &alerts))
			outcome, err := gate.Screen(context.Background(), VisualScreenInput{
				Mime:     "image/png",
				RawBytes: raw,
			})
			testutil.FailErr(t, "gate.Screen", err)
			if len(alerts) > 0 && outcome.Perception == PerceptionRedacted {
				detectedAtSize++
			}
		}
		totalRuns += len(twentyFourLiveOCRSecrets)
		totalExact += exactAtSize
		totalDetected += detectedAtSize
		rate := float64(detectedAtSize) / float64(len(twentyFourLiveOCRSecrets))
		t.Logf("%.0fpt 24-secret corpus: exact=%d/%d (%.1f%%), confusion-tolerant detected=%d/%d (%.1f%%)",
			pt, exactAtSize, len(twentyFourLiveOCRSecrets),
			float64(exactAtSize)*100/float64(len(twentyFourLiveOCRSecrets)),
			detectedAtSize, len(twentyFourLiveOCRSecrets), rate*100)
		if rate < 0.90 {
			t.Errorf("%.0fpt detection rate = %.1f%% (%d/%d), want >= 90%%",
				pt, rate*100, detectedAtSize, len(twentyFourLiveOCRSecrets))
		}
	}
	overallRate := float64(totalDetected) / float64(totalRuns)
	t.Logf("overall 24-secret corpus across 18/28/40pt: exact=%d/%d (%.1f%%), confusion-tolerant=%d/%d (%.1f%%)",
		totalExact, totalRuns, float64(totalExact)*100/float64(totalRuns),
		totalDetected, totalRuns, overallRate*100)
}

func TestPlatformEngineNonSecretScreenshotCorpusZeroFalsePositives(t *testing.T) {
	engine := platformEngine(t)
	matcher := bundledMatcher(t)
	nonSecretScreenshots := [][]string{
		{"Painted Wolf Code", "File: /Users/dev/project/internal/visualscreen/scanner.go"},
		{"https://github.com/paintedwolf/lycaon/pull/785", "status: 200 OK (142ms)"},
		{"commit 01bb82ccac9f4e3d2a1b0c9d8e7f6a5b4c3d2e1f", "Author: Developer <dev@example.com>"},
		{"UUID: 550e8400-e29b-41d4-a716-446655440000", " export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE"},
	}
	for _, pt := range []float64{18, 28, 40} {
		for i, lines := range nonSecretScreenshots {
			raw, _ := renderPNG(t, ocrfixture.Options{
				Lines:     lines,
				PointSize: pt,
			})
			var alerts []secretmatch.Alert
			gate := NewGate(NewScanner(engine), matcher, decide(secretmatch.SendRedacted, &alerts))
			outcome, err := gate.Screen(context.Background(), VisualScreenInput{
				Mime:     "image/png",
				RawBytes: raw,
			})
			testutil.FailErr(t, "gate.Screen", err)
			if len(alerts) != 0 || outcome.Perception != PerceptionOriginal {
				t.Errorf("%.0fpt non-secret screenshot %d triggered false positive: alerts=%+v text=%q",
					pt, i, alerts, outcome.ExtractedText)
			}
		}
	}
}
