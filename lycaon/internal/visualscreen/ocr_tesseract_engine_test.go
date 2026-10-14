package visualscreen

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"math"
	"runtime"
	"strings"
	"testing"
	"unicode"

	"github.com/lycaon/lycaon/internal/ocrengine/ocrtest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visualscreen/ocrfixture"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// engineSecret is an AWS-shaped key built without the glyphs the recognizers
// confuse in the fixture face (0/O, 1/I/l, 5/S, 7), so engine tests measure
// reading geometry and masking rather than glyph ambiguity.
const engineSecret = "AKIAQYJK4TXV6NHRWMFD"

// platformEngine is the recognizer shipped for this platform: Apple Vision on
// macOS, the bundled Tesseract engine on Linux when it is staged.
func platformEngine(t *testing.T) OCREngine {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
	case "linux":
		ocrtest.SkipIfNoEngine(t)
	default:
		t.Skip("no text recognizer on this platform")
	}
	return NewDefaultOCREngine()
}

// bundledTesseract is the Linux engine alone; rotation handling is its contract.
func bundledTesseract(t *testing.T) OCREngine {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the bundled engine ships on Linux")
	}
	ocrtest.SkipIfNoEngine(t)
	return NewDefaultOCREngine()
}

func renderPNG(t *testing.T, opts ocrfixture.Options) ([]byte, []ocrfixture.Line) {
	t.Helper()
	img, lines, err := ocrfixture.Render(opts)
	if err != nil {
		testutil.FailErr(t, "render fixture", err)
	}
	raw, err := ocrfixture.PNG(img)
	if err != nil {
		testutil.FailErr(t, "encode fixture", err)
	}
	return raw, lines
}

func recognize(t *testing.T, engine OCREngine, raw []byte) []TextSpan {
	t.Helper()
	spans, err := engine.RecognizeText(context.Background(), "image/png", raw)
	if err != nil {
		testutil.FailErr(t, "recognize", err)
	}
	return spans
}

// foldText lowers case, strips diacritics, and keeps letters and digits, so a
// comparison measures reading rather than typography.
func foldText(s string) string {
	stripped, _, _ := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	var b strings.Builder
	for _, r := range strings.ToLower(stripped) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// editDistance is the Levenshtein distance between two strings.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// spanFor finds the recognized span closest in text to want and asserts it
// reads within tolerance and lies where the fixture drew the line.
func spanFor(t *testing.T, spans []TextSpan, want ocrfixture.Line, maxErrorRate float64) TextSpan {
	t.Helper()
	wantText := foldText(want.Text)
	best, bestDist := TextSpan{}, math.MaxInt
	for _, span := range spans {
		if d := editDistance(foldText(span.Text), wantText); d < bestDist {
			best, bestDist = span, d
		}
	}
	if bestDist == math.MaxInt || float64(bestDist) > maxErrorRate*float64(len(wantText)) {
		t.Fatalf("line %q not read (closest %q, %d edits) in %+v", want.Text, best.Text, bestDist, spans)
	}
	got, exp := best.BoundingBox, want.BoundingBox
	// The recognized box lies inside the drawn line's box, within a 4% margin, and
	// covers most of it.
	const margin = 0.04
	if got[0] < exp[0]-margin || got[1] < exp[1]-margin || got[0]+got[2] > exp[0]+exp[2]+margin || got[1]+got[3] > exp[1]+exp[3]+margin {
		t.Fatalf("line %q box %v lies outside the drawn box %v", want.Text, got, exp)
	}
	if got[2] < 0.6*exp[2] || got[3] < 0.5*exp[3] {
		t.Fatalf("line %q box %v covers too little of the drawn box %v", want.Text, got, exp)
	}
	if best.Confidence < 0 || best.Confidence > 1 {
		t.Fatalf("confidence %v outside [0,1]", best.Confidence)
	}
	return best
}

func TestPlatformEngineReadsLinesWithGeometry(t *testing.T) {
	engine := platformEngine(t)
	raw, lines := renderPNG(t, ocrfixture.Options{Lines: []string{"Painted Wolf Code", "deploy key " + engineSecret}})
	spans := recognize(t, engine, raw)
	for _, line := range lines {
		spanFor(t, spans, line, 0.1)
	}
}

func TestPlatformEngineReadsNothingFromBlankRaster(t *testing.T) {
	engine := platformEngine(t)
	raw, _ := renderPNG(t, ocrfixture.Options{})
	if spans := recognize(t, engine, raw); len(spans) != 0 {
		t.Fatalf("blank raster read %+v", spans)
	}
	if spans := recognize(t, engine, whitePNG(t, 4, 4)); len(spans) != 0 {
		t.Fatalf("tiny blank raster read %+v", spans)
	}
}

func TestPlatformEngineReadsAcrossScales(t *testing.T) {
	engine := platformEngine(t)
	for name, opts := range map[string]ocrfixture.Options{
		// Apple Vision reads this face reliably from 14 pt; smaller type is dropped or misread.
		"small": {Lines: []string{"small print 12345"}, PointSize: 14, Padding: 6},
		"large": {Lines: []string{"LARGE HEADING"}, PointSize: 72},
		"dark":  {Lines: []string{"light on dark"}, Inverted: true},
	} {
		t.Run(name, func(t *testing.T) {
			raw, lines := renderPNG(t, opts)
			spans := recognize(t, engine, raw)
			spanFor(t, spans, lines[0], 0.15)
		})
	}
}

// Latin-script text with diacritics reads as its letters; the English model
// may drop or confuse the marks, which the fold tolerates.
func TestPlatformEngineReadsLatinScriptText(t *testing.T) {
	engine := platformEngine(t)
	raw, lines := renderPNG(t, ocrfixture.Options{Lines: []string{"Café déjà vu in Zürich", "naïve façade à São Paulo"}})
	spans := recognize(t, engine, raw)
	for _, line := range lines {
		spanFor(t, spans, line, 0.25)
	}
}

func TestBundledTesseractReadsRotatedCaptures(t *testing.T) {
	engine := bundledTesseract(t)
	for turns := 1; turns <= 3; turns++ {
		raw, lines := renderPNG(t, ocrfixture.Options{Lines: []string{"Painted Wolf Code", "rotated capture of a key"}, QuarterTurns: turns})
		spans := recognize(t, engine, raw)
		for _, line := range lines {
			spanFor(t, spans, line, 0.15)
		}
	}
}

// A raster whose header decodes but whose pixel data is cut short comes back
// from the bundled engine as a recognition failure, never as a clean read.
func TestBundledTesseractTruncatedRasterIsFailedNotClean(t *testing.T) {
	engine := bundledTesseract(t)
	raw, _ := renderPNG(t, ocrfixture.Options{Lines: []string{"Painted Wolf Code"}})
	truncated := raw[:len(raw)*2/3]
	scanned, err := NewScanner(engine).Scan(context.Background(), "image/png", truncated)
	if err != nil {
		testutil.FailErr(t, "scan", err)
	}
	if scanned.Gap != secretmatch.GapOCRFailed || scanned.GapDetail == "" {
		t.Fatalf("scanned = %+v", scanned)
	}
	if _, err := engine.RecognizeText(context.Background(), "image/png", truncated); err == nil || errors.Is(err, ErrOCRUnavailable) {
		t.Fatalf("truncated raster err = %v", err)
	}
}

// The gate masks the span the engine read the credential from, and the masked
// raster no longer yields it.
func TestPlatformEngineGateMasksRecognizedSecret(t *testing.T) {
	engine := platformEngine(t)
	raw, lines := renderPNG(t, ocrfixture.Options{Lines: []string{"Public heading", "aws key " + engineSecret}})
	var seen []secretmatch.Alert
	outcome, err := NewGate(NewScanner(engine), bundledMatcher(t), decide(secretmatch.SendRedacted, &seen)).Screen(context.Background(), VisualScreenInput{
		Mime: "image/png", RawBytes: raw,
	})
	if err != nil {
		testutil.FailErr(t, "screen", err)
	}
	if len(seen) != 1 || seen[0].ScreeningGap != "" || !seen[0].CanRedact() {
		t.Fatalf("alerts = %+v", seen)
	}
	if outcome.Perception != PerceptionRedacted || strings.Contains(outcome.ExtractedText, engineSecret) {
		t.Fatalf("outcome = %+v", outcome)
	}
	masked, err := png.Decode(bytes.NewReader(outcome.PerceiveBytes))
	if err != nil {
		testutil.FailErr(t, "decode masked", err)
	}
	b := masked.Bounds()
	heading, key := lines[0].BoundingBox, lines[1].BoundingBox
	probe := func(box [4]float64) uint32 {
		x := b.Min.X + int((box[0]+box[2]/2)*float64(b.Dx()))
		y := b.Min.Y + int((box[1]+box[3]/2)*float64(b.Dy()))
		r, _, _, _ := masked.At(x, y).RGBA()
		return r
	}
	if probe(key) != 0 {
		t.Fatal("the credential line was not masked")
	}
	if probe(heading) == 0 {
		t.Fatal("the public heading was masked")
	}
	for _, span := range recognize(t, engine, outcome.PerceiveBytes) {
		if strings.Contains(foldText(span.Text), foldText(engineSecret)) {
			t.Fatalf("masked raster still reads the credential: %+v", span)
		}
	}
}
