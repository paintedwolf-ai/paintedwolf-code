package captureprojection

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

const testSecret = "capture-secret-value"

func testProjector(t *testing.T, primer ManagedSecretPrimer) *Projector {
	t.Helper()
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Name: "token", Secret: testSecret,
			RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
		}}
	})
	return New(m, primer)
}

func TestProjectionFailsClosedWithoutAMatcher(t *testing.T) {
	p := New(nil, nil)
	if _, err := p.Text(t.Context(), Scope{}, "capture.test", "raw"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("text error = %v, want unavailable", err)
	}
	if _, _, err := p.JSON(t.Context(), Scope{}, "capture.test", []byte(`{"raw":true}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("JSON error = %v, want unavailable", err)
	}
	if _, _, err := p.Grid(t.Context(), Scope{}, "capture.test", []string{"raw"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("grid error = %v, want unavailable", err)
	}
	if _, _, err := p.Raster(t.Context(), Scope{}, "image/png", []byte("raw"), 1, 1, nil, true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("raster error = %v, want unavailable", err)
	}
}

func TestTextAndGridProjectExactSecretWithoutChangingGridGeometry(t *testing.T) {
	p := testProjector(t, nil)
	ctx := t.Context()
	scope := Scope{ProjectID: "project", RootSessionID: "root", SessionID: "session"}

	text, err := p.Text(ctx, scope, "capture.process", "before "+testSecret+" after")
	testutil.FailErr(t, "project text", err)
	if strings.Contains(text.Value, testSecret) || text.Metadata.RedactedCount != 1 {
		t.Fatalf("text projection = %q metadata=%+v", text.Value, text.Metadata)
	}

	lines := []string{"before " + testSecret, "after"}
	safe, meta, err := p.Grid(ctx, scope, "capture.terminal", lines)
	testutil.FailErr(t, "project grid", err)
	if strings.Contains(strings.Join(safe, "\n"), testSecret) || meta.RedactedCount != 1 {
		t.Fatalf("grid projection = %q metadata=%+v", safe, meta)
	}
	if len([]rune(safe[0])) != len([]rune(lines[0])) || len(safe) != len(lines) {
		t.Fatalf("grid geometry changed: before=%q after=%q", lines, safe)
	}
}

func horizontalRuneSpans(text string, x, y, width, height float64) []TextSpan {
	runes := []rune(text)
	spans := make([]TextSpan, len(runes))
	for i := range runes {
		spans[i] = TextSpan{
			Start: i, End: i + 1,
			Rects: []Rect{{X: x + float64(i)*width, Y: y, Width: width, Height: height}},
		}
	}
	return spans
}

func TestRasterMasksOnlyTheMatchedTextSpans(t *testing.T) {
	p := testProjector(t, nil)
	raw := image.NewRGBA(image.Rect(0, 0, 320, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 320; x++ {
			raw.Set(x, y, color.White)
		}
	}
	var encoded bytes.Buffer
	testutil.FailErr(t, "encode raster fixture", png.Encode(&encoded, raw))

	text := "safe prefix " + testSecret + " safe suffix"
	out, meta, err := p.Raster(t.Context(), Scope{}, "image/png", encoded.Bytes(), 320, 60, []Region{{
		Text:  text,
		Spans: horizontalRuneSpans(text, 10, 12, 7, 18),
	}}, true)
	testutil.FailErr(t, "project raster", err)
	if meta.RedactedCount != 1 || !meta.StructuredCoverage {
		t.Fatalf("raster metadata = %+v", meta)
	}
	projected, err := png.Decode(bytes.NewReader(out))
	testutil.FailErr(t, "decode raster projection", err)
	secretStart := 10 + len([]rune("safe prefix "))*7
	if got := color.RGBAModel.Convert(projected.At(secretStart+3, 20)).(color.RGBA); got == (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("secret region was not masked: %v", got)
	}
	for _, point := range []image.Point{{X: 13, Y: 20}, {X: 310, Y: 20}, {X: secretStart - 2, Y: 20}} {
		if got := color.RGBAModel.Convert(projected.At(point.X, point.Y)).(color.RGBA); got != (color.RGBA{255, 255, 255, 255}) {
			t.Fatalf("unmatched pixel at %v changed: %v", point, got)
		}
	}
}

func TestRasterMasksASecretSplitAcrossAdjacentDOMRegions(t *testing.T) {
	p := testProjector(t, nil)
	raw := image.NewRGBA(image.Rect(0, 0, 100, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 100; x++ {
			raw.Set(x, y, color.White)
		}
	}
	var encoded bytes.Buffer
	testutil.FailErr(t, "encode split-region fixture", png.Encode(&encoded, raw))

	out, meta, err := p.Raster(t.Context(), Scope{}, "image/png", encoded.Bytes(), 100, 30, []Region{
		{Text: "capture-", Spans: horizontalRuneSpans("capture-", 5, 5, 4, 15)},
		{Text: "secret-value", Spans: horizontalRuneSpans("secret-value", 40, 5, 4, 15)},
	}, true)
	testutil.FailErr(t, "project split-region raster", err)
	if meta.RedactedCount != 1 {
		t.Fatalf("raster metadata = %+v", meta)
	}
	projected, err := png.Decode(bytes.NewReader(out))
	testutil.FailErr(t, "decode split-region projection", err)
	for _, point := range []image.Point{{X: 10, Y: 10}, {X: 60, Y: 10}} {
		if got := color.RGBAModel.Convert(projected.At(point.X, point.Y)).(color.RGBA); got == (color.RGBA{255, 255, 255, 255}) {
			t.Fatalf("split secret region at %v was not masked: %v", point, got)
		}
	}
}

func TestRasterScalesMasksAgainstTheCapturedCSSExtent(t *testing.T) {
	p := testProjector(t, nil)
	raw := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			raw.Set(x, y, color.White)
		}
	}
	var encoded bytes.Buffer
	testutil.FailErr(t, "encode scaled raster fixture", png.Encode(&encoded, raw))

	out, _, err := p.Raster(t.Context(), Scope{}, "image/png", encoded.Bytes(), 100, 200, []Region{{
		Text: testSecret, Spans: horizontalRuneSpans(testSecret, 10, 100, 2, 20),
	}}, true)
	testutil.FailErr(t, "project scaled raster", err)
	projected, err := png.Decode(bytes.NewReader(out))
	testutil.FailErr(t, "decode scaled projection", err)
	if got := color.RGBAModel.Convert(projected.At(20, 55)).(color.RGBA); got == (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("scaled secret region was not masked: %v", got)
	}
	if got := color.RGBAModel.Convert(projected.At(20, 25)).(color.RGBA); got != (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("unrelated scaled pixel changed: %v", got)
	}
}

func TestJSONScreensStringValuesAndKeys(t *testing.T) {
	p := testProjector(t, nil)
	out, meta, err := p.JSON(t.Context(), Scope{}, "capture.semantic", []byte(`{
		"safe":"before capture-secret-value after",
		"capture-secret-value":"secret key"
	}`))
	testutil.FailErr(t, "project captured JSON", err)
	if strings.Contains(string(out), testSecret) || meta.RedactedCount != 2 {
		t.Fatalf("JSON projection = %s metadata=%+v", out, meta)
	}
}

func TestJSONRejectsTrailingValues(t *testing.T) {
	p := testProjector(t, nil)
	if _, _, err := p.JSON(t.Context(), Scope{}, "capture.semantic", []byte(`{"safe":true} {"extra":true}`)); err == nil {
		t.Fatal("multiple captured JSON values were accepted")
	}
}

func TestRasterPreservesEveryPixelOutsideMatchedText(t *testing.T) {
	p := testProjector(t, nil)
	raw := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			raw.Set(x, y, color.RGBA{R: uint8(x * 11), G: uint8(y * 11), B: uint8((x + y) * 6), A: 255})
		}
	}
	var encoded bytes.Buffer
	testutil.FailErr(t, "encode pixel-preservation fixture", png.Encode(&encoded, raw))
	out, meta, err := p.Raster(t.Context(), Scope{}, "image/png", encoded.Bytes(), 20, 20, []Region{{
		Text: "game surface", Spans: horizontalRuneSpans("game surface", 0, 0, 1, 1),
	}}, true)
	testutil.FailErr(t, "project pixel-preservation raster", err)
	if meta.RedactedCount != 0 || !meta.StructuredCoverage {
		t.Fatalf("raster metadata = %+v", meta)
	}
	projected, err := png.Decode(bytes.NewReader(out))
	testutil.FailErr(t, "decode pixel-preservation projection", err)
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			want := color.RGBAModel.Convert(raw.At(x, y)).(color.RGBA)
			if got := color.RGBAModel.Convert(projected.At(x, y)).(color.RGBA); got != want {
				t.Fatalf("unmatched pixel (%d,%d) = %v want %v", x, y, got, want)
			}
		}
	}
}

func TestRasterNeverUsesCoarseGeometryAsAMaskingFallback(t *testing.T) {
	p := testProjector(t, nil)
	raw := image.NewRGBA(image.Rect(0, 0, 40, 20))
	draw.Draw(raw, raw.Bounds(), &image.Uniform{C: color.RGBA{R: 215, G: 38, B: 61, A: 255}}, image.Point{}, draw.Src)
	var encoded bytes.Buffer
	testutil.FailErr(t, "encode coarse geometry fixture", png.Encode(&encoded, raw))

	out, meta, err := p.Raster(t.Context(), Scope{}, "image/png", encoded.Bytes(), 40, 20, []Region{{
		Text: testSecret,
		Spans: []TextSpan{{
			Start: 0, End: len([]rune(testSecret)),
			Rects: []Rect{{X: 0, Y: 0, Width: 40, Height: 20}},
		}},
	}}, true)
	testutil.FailErr(t, "project coarse geometry fixture", err)
	if !bytes.Equal(out, encoded.Bytes()) {
		t.Fatal("coarse geometry changed capture bytes")
	}
	if meta.RedactedCount != 0 || meta.StructuredCoverage {
		t.Fatalf("coarse geometry metadata = %+v", meta)
	}
}

func TestRasterDoesNotClaimRedactionWithoutMatchedGeometry(t *testing.T) {
	p := testProjector(t, nil)
	raw := image.NewRGBA(image.Rect(0, 0, 240, 20))
	draw.Draw(raw, raw.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	var encoded bytes.Buffer
	testutil.FailErr(t, "encode incomplete geometry fixture", png.Encode(&encoded, raw))

	text := "safe " + testSecret
	out, meta, err := p.Raster(t.Context(), Scope{}, "image/png", encoded.Bytes(), 240, 20, []Region{{
		Text:  text,
		Spans: horizontalRuneSpans("safe", 0, 0, 8, 16),
	}}, true)
	testutil.FailErr(t, "project incomplete geometry fixture", err)
	if !bytes.Equal(out, encoded.Bytes()) {
		t.Fatal("unmatched geometry changed capture bytes")
	}
	if meta.RedactedCount != 0 || meta.StructuredCoverage {
		t.Fatalf("incomplete geometry metadata = %+v", meta)
	}
}

func TestManagedSecretPrimerCachesPerTaskTreeAndRefreshesGeneration(t *testing.T) {
	var calls atomic.Int32
	var generation atomic.Uint64
	generation.Store(1)
	p := testProjector(t, func(context.Context, string, string) error {
		calls.Add(1)
		return nil
	})
	p.SetManagedSecretGeneration(generation.Load)
	scope := Scope{ProjectID: "project", RootSessionID: "root"}
	for range 3 {
		_, err := p.Text(t.Context(), scope, "capture", "safe")
		testutil.FailErr(t, "project cached text", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("primer calls = %d, want 1", got)
	}
	generation.Add(1)
	_, err := p.Text(t.Context(), scope, "capture", "safe")
	testutil.FailErr(t, "project text after value change", err)
	if got := calls.Load(); got != 2 {
		t.Fatalf("primer calls after value change = %d, want 2", got)
	}
}

// disclosableProjector keeps ordinary word-boundary matching active.
func disclosableProjector(t *testing.T) *Projector {
	t.Helper()
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Name: "token", Secret: testSecret,
			RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch,
		}}
	})
	return New(m, nil)
}

func TestGridMasksAValueThatWrapsToTheNextRow(t *testing.T) {
	p := testProjector(t, nil)
	// Neither physical row contains the complete value.
	lines := []string{
		"ready       ",
		testSecret[:12],
		testSecret[12:] + "    ",
	}
	safe, meta, err := p.Grid(t.Context(), Scope{}, "capture.terminal", lines)
	testutil.FailErr(t, "project wrapped grid", err)
	for _, half := range []string{testSecret[:12], testSecret[12:]} {
		if strings.Contains(strings.Join(safe, ""), half) {
			t.Fatalf("wrapped value survived projection: %q", safe)
		}
	}
	if meta.RedactedCount != 2 {
		t.Fatalf("redacted runs = %d, want one per row the value crossed", meta.RedactedCount)
	}
	if len(safe) != len(lines) {
		t.Fatalf("row count changed: %d want %d", len(safe), len(lines))
	}
	for i := range lines {
		if len([]rune(safe[i])) != len([]rune(lines[i])) {
			t.Fatalf("row %d width changed: %q want %d columns", i, safe[i], len([]rune(lines[i])))
		}
	}
	if safe[0] != lines[0] {
		t.Fatalf("unrelated row changed: %q", safe[0])
	}
}

func TestGridKeepsRowBoundedMatchingWhenRowsAreFull(t *testing.T) {
	p := testProjector(t, nil)
	lines := []string{testSecret + "!", "unrelated line here!!"}
	safe, _, err := p.Grid(t.Context(), Scope{}, "capture.terminal", lines)
	testutil.FailErr(t, "project row-bounded grid", err)
	if strings.Contains(safe[0], testSecret) {
		t.Fatalf("row-local value survived: %q", safe[0])
	}
	if safe[1] != lines[1] {
		t.Fatalf("unrelated row changed: %q", safe[1])
	}
}

func TestRasterMasksAValueFusedWithTheRegionBeforeIt(t *testing.T) {
	p := disclosableProjector(t)
	raw := image.NewRGBA(image.Rect(0, 0, 100, 30))
	for y := range 30 {
		for x := range 100 {
			raw.Set(x, y, color.White)
		}
	}
	var encoded bytes.Buffer
	testutil.FailErr(t, "encode fused-region fixture", png.Encode(&encoded, raw))
	// Joined regions can hide a word-bounded match.
	out, meta, err := p.Raster(t.Context(), Scope{}, "image/png", encoded.Bytes(), 100, 30, []Region{
		{Text: "API key", Spans: horizontalRuneSpans("API key", 5, 5, 4, 15)},
		{Text: testSecret, Spans: horizontalRuneSpans(testSecret, 40, 5, 2, 15)},
	}, true)
	testutil.FailErr(t, "project fused-region raster", err)
	if meta.RedactedCount != 1 {
		t.Fatalf("raster metadata = %+v", meta)
	}
	projected, err := png.Decode(bytes.NewReader(out))
	testutil.FailErr(t, "decode fused-region projection", err)
	if got := color.RGBAModel.Convert(projected.At(60, 10)).(color.RGBA); got == (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("fused value region was not masked: %v", got)
	}
}

func TestTextSpansReportTheInputRangesItMasked(t *testing.T) {
	p := testProjector(t, nil)
	value := "prefix " + testSecret + " suffix"
	projected, spans, err := p.TextSpans(t.Context(), Scope{}, "capture.process", value)
	testutil.FailErr(t, "project text spans", err)
	if strings.Contains(projected.Value, testSecret) {
		t.Fatalf("projection = %q", projected.Value)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want one", spans)
	}
	if got := value[spans[0].Start:spans[0].End]; got != testSecret {
		t.Fatalf("span covers %q, want %q", got, testSecret)
	}
}

func TestScopeForFallsBackToTheSessionAsItsOwnRoot(t *testing.T) {
	scope := ScopeFor("project", "", "session")
	if scope.RootSessionID != "session" {
		t.Fatalf("root session = %q, want the session's own id", scope.RootSessionID)
	}
	if got := ScopeFor("project", "parent", "session"); got.RootSessionID != "parent" {
		t.Fatalf("root session = %q, want the parent", got.RootSessionID)
	}
}
