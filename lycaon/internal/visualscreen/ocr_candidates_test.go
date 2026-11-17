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
