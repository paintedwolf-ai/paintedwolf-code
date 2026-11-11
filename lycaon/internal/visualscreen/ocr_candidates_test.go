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
		"5\t1\t1\t1\t1\t2\t60\t20\t120\t12\t58.0\tTOKEN=AKIAQ7Z2M4N6P3R6T5V7\n" +
		"5\t1\t1\t1\t1\t2\t60\t20\t120\t12\t55.0\tTOKEN=AK1AQ7Z2M4N6P3R6T5V7\n" +
		"5\t1\t1\t1\t2\t1\t10\t40\t40\t12\t95.0\texport\n" +
		"5\t1\t1\t1\t2\t2\t60\t40\t90\t12\t88.0\tSECRET=AKIAL6K4J2H/7G5F3D2S7T\n" +
		"5\t1\t1\t1\t2\t3\t152\t40\t30\t12\t86.0\t41207\n"
	spans, err := parseTesseractTSV([]byte(tsv), 200, 100)
	testutil.FailErr(t, "parseTesseractTSV", err)
	if len(spans) != 2 {
		t.Fatalf("len(spans) = %d, want 2", len(spans))
	}
	if spans[0].Text != "export TOKEN=blurred" {
		t.Fatalf("Text = %q, want %q", spans[0].Text, "export TOKEN=blurred")
	}
	wantCandidates := []string{
		"export TOKEN=AKIAQ7Z2M4N6P3R6T5V7",
		"export TOKEN=AK1AQ7Z2M4N6P3R6T5V7",
		"exportTOKEN=blurred",
	}
	if len(spans[0].Candidates) != len(wantCandidates) {
		t.Fatalf("Candidates = %#v, want %#v", spans[0].Candidates, wantCandidates)
	}
	for i, want := range wantCandidates {
		if spans[0].Candidates[i] != want {
			t.Fatalf("Candidates[%d] = %q, want %q", i, spans[0].Candidates[i], want)
		}
	}
	wantRepaired := "exportSECRET=AKIAL6K4J2H7G5F3D2S7T41207"
	foundRepaired := false
	for _, cand := range spans[1].Candidates {
		if cand == wantRepaired {
			foundRepaired = true
			break
		}
	}
	if !foundRepaired {
		t.Fatalf("spans[1].Candidates = %#v, expected repaired candidate %q", spans[1].Candidates, wantRepaired)
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
			name: "empty primary retains nonempty candidate and its pixel bounds",
			span: TextSpan{Text: " \t", Candidates: []string{" ", "export AWS_KEY=AKlAQ7Z2M4N6P3R6T5V/"}, BoundingBox: lines[1].BoundingBox},
		},
		{
			name: "primary text with OCR confusions in prefix and trailing slash",
			span: TextSpan{
				Text:        "export AWS_KEY=AK1AQ7Z2M4N6P3R6T5V/",
				Confidence:  0.9,
				BoundingBox: lines[1].BoundingBox,
			},
		},
		{
			name: "non-secret primary text rescued by alternative OCR candidate",
			span: TextSpan{
				Text:        "export AWS_KEY=unrecognized_blur",
				Candidates:  []string{"export AWS_KEY=AKlAQ7Z2M4N6P3R6T5V/"},
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
				strings.Contains(outcome.ExtractedText, "Q7Z2M4N6P3R6T5V") {
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
	const confusedToken = "AK1AQ7Z2M4N6P3R6T5V/"

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
	"AKIAQ7W2E5R3T6Y4I2O7",
	"AKIAZ3X5C7V2B4N6M3O5",
	"AKIAP2O4I6U3Y5T7R2E4",
	"AKIAL6K4J2H7G5F3D2S7",
	"AKIAM3N5B7V2C4X6Z5I3",
	"AKIAW5E7R2T4Y6U3O5S2",
	"AKIAD4F6G2H5J7K3L2I6",
	"AKIAF7G3H5J2K6L4O7S3",
	"AKIAT2Y4U6I3O5P7A2S4",
	"AKIAR5T7Y2U4I6O3P5S7",
	"AKIAV6B2N4M7Q3W5E2O6",
	"ghp_K4g5F2x8S3E4t6g3D0N2n7z6G4M8p2s5C0q3",
	"ghp_R7t5Y2u4I6o3P8a1S9d0F2g4H6k8K3l5Z7x9",
	"ghp_M2n4B6v8C0x1Z3l5K7m9H2g4F6d8S0a1P3o5",
	"ghp_Q9w7E5r3T1y2U4o6O8p0L3k5M7h9G2f4D6s8",
	"ghp_Z1x3C5v7B9n2M4q6W8e0R1t3Y5u7I9o2P4a6",
	"ghp_A8s6D4f2G0h1K3k5L7z9X2c4V6b8N0m3Q5w7",
	"ghp_P0o8I6u4Y2t1R3e5W7q9A2s4D6f8G0h1K3k5",
	"ghp_L5k3M1h9G7f2D4s6A8p0O1u3U5y7T9r2E4w6",
	"ghp_N6m4Q2w8E0r1T3y5U7o9O2p4A6s8D0f1G3h5",
	"ghp_V7b5N3m1Q9w2E4r6T8y0U1o3O5p7A9s2D4f6",
	"ghp_C4v6B8n0M2q1W3e5R7t9Y0u2I4o6P8a1S3d5",
	"ghp_X3z5L7k9M1h2G4f6D8s0A2p4O6u8U0y1T3r5",
}

func TestPlatformEngineOCRConfusionSecretCorpusAcrossFontSizes(t *testing.T) {
	assertEngineOCRConfusionSecretCorpus(t, platformEngine(t))
}

func assertEngineOCRConfusionSecretCorpus(t *testing.T, engine OCREngine) {
	t.Helper()
	matcher := bundledMatcher(t)
	pointSizes := []float64{18, 28, 40}
	const numBatches = 4
	totalRuns, totalExact, totalDetected := 0, 0, 0
	for _, pt := range pointSizes {
		exactAtSize, detectedAtSize := 0, 0
		for b := 0; b < numBatches; b++ {
			var batch []string
			for i := b; i < len(twentyFourLiveOCRSecrets); i += numBatches {
				batch = append(batch, twentyFourLiveOCRSecrets[i])
			}
			renderLines := make([]string, 0, len(batch)+1)
			renderLines = append(renderLines, "Painted Wolf Code")
			for _, secret := range batch {
				renderLines = append(renderLines, "export SECRET="+secret)
			}
			raw, drawnLines := renderPNG(t, ocrfixture.Options{
				Lines:     renderLines,
				PointSize: pt,
			})
			spans := recognize(t, engine, raw)
			for i, secret := range batch {
				span, ok := matchSpanByVerticalCenter(spans, drawnLines[i+1])
				if !ok {
					t.Logf("[%.0fpt MISS] no span for line %d (%s)", pt, i+1, secret)
					continue
				}
				if strings.Contains(span.Text, secret) && len(matcher.Screen(span.Text)) > 0 {
					exactAtSize++
				}
				var alerts []secretmatch.Alert
				lineEngine := &fakeOCR{spans: []TextSpan{span}}
				gate := NewGate(NewScanner(lineEngine), matcher, decide(secretmatch.SendRedacted, &alerts))
				outcome, err := gate.Screen(context.Background(), VisualScreenInput{
					Mime:     "image/png",
					RawBytes: raw,
				})
				testutil.FailErr(t, "gate.Screen", err)
				if len(alerts) > 0 && outcome.Perception == PerceptionRedacted {
					detectedAtSize++
				} else {
					t.Logf("[%.0fpt MISS] want=%q got=%q candidates=%q", pt, "export SECRET="+secret, span.Text, span.Candidates)
				}
			}
		}
		totalRuns += len(twentyFourLiveOCRSecrets)
		totalExact += exactAtSize
		totalDetected += detectedAtSize
		rate := float64(detectedAtSize) / float64(len(twentyFourLiveOCRSecrets))
		minRate := 0.90
		if pt <= 18 {
			minRate = 0.85
		}
		t.Logf("%.0fpt 24-secret corpus: exact=%d/%d (%.1f%%), confusion-tolerant detected=%d/%d (%.1f%%)",
			pt, exactAtSize, len(twentyFourLiveOCRSecrets),
			float64(exactAtSize)*100/float64(len(twentyFourLiveOCRSecrets)),
			detectedAtSize, len(twentyFourLiveOCRSecrets), rate*100)
		if rate < minRate {
			t.Errorf("%.0fpt detection rate = %.1f%% (%d/%d), want >= %.0f%%",
				pt, rate*100, detectedAtSize, len(twentyFourLiveOCRSecrets), minRate*100)
		}
	}
	overallRate := float64(totalDetected) / float64(totalRuns)
	t.Logf("overall 24-secret corpus across 18/28/40pt: exact=%d/%d (%.1f%%), confusion-tolerant=%d/%d (%.1f%%)",
		totalExact, totalRuns, float64(totalExact)*100/float64(totalRuns),
		totalDetected, totalRuns, overallRate*100)
	if overallRate < 0.90 {
		t.Errorf("overall detection rate = %.1f%% (%d/%d), want >= 90%%",
			overallRate*100, totalDetected, totalRuns)
	}
}

func matchSpanByVerticalCenter(spans []TextSpan, want ocrfixture.Line) (TextSpan, bool) {
	wantY := want.BoundingBox[1] + want.BoundingBox[3]/2
	bestIdx := -1
	bestDist := 1.0
	for i, s := range spans {
		gotY := s.BoundingBox[1] + s.BoundingBox[3]/2
		dist := gotY - wantY
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist && dist <= want.BoundingBox[3] {
			bestDist = dist
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return TextSpan{}, false
	}
	return spans[bestIdx], true
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
