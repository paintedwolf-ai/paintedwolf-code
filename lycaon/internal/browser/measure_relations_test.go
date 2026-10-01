package browser

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGapBetween_horizontal(t *testing.T) {
	a := ElementRect{Left: 40, Right: 160, Top: 40, Bottom: 100, Width: 120, Height: 60, X: 40, Y: 40}
	b := ElementRect{Left: 180, Right: 260, Top: 40, Bottom: 100, Width: 80, Height: 60, X: 180, Y: 40}
	if g := gapBetween(a, b); g != 20 {
		t.Fatalf("gap = %v want 20", g)
	}
	if axis := gapAxis(a, b); axis != "horizontal" {
		t.Fatalf("axis = %q want horizontal", axis)
	}
}

func TestDeriveMeasureRelations_alignmentAndContrast(t *testing.T) {
	els := []MeasuredElement{
		{
			Selector: "#box-a",
			Rect:     ElementRect{Left: 40, Right: 160, Top: 40, Bottom: 100, Width: 120, Height: 60, X: 40, Y: 40},
			Styles:   map[string]string{"color": "rgb(255, 255, 255)", "background-color": "rgb(0, 0, 0)"},
		},
		{
			Selector: "#box-b",
			Rect:     ElementRect{Left: 180, Right: 260, Top: 40, Bottom: 100, Width: 80, Height: 60, X: 180, Y: 40},
			Styles:   map[string]string{"color": "rgb(0, 0, 0)", "background-color": "rgb(204, 204, 204)"},
		},
	}
	rels := DeriveMeasureRelations(els, 1280, 720)
	var sawGap, sawAlignTop, sawContrast bool
	for _, r := range rels {
		switch r.Kind {
		case "gap":
			if r.Value == 20 {
				sawGap = true
			}
		case "alignment":
			if shared, _ := r.Detail["shared"].(string); shared == "top" {
				sawAlignTop = true
			}
		case "contrast":
			if r.Of == "#box-a" && r.Value >= 20 {
				sawContrast = true
			}
		}
	}
	if !sawGap || !sawAlignTop || !sawContrast {
		t.Fatalf("missing relations: gap=%v align=%v contrast=%v (rels=%+v)", sawGap, sawAlignTop, sawContrast, rels)
	}
}

func TestWCAGContrast_blackOnWhite(t *testing.T) {
	ratio, ok := wcagContrastRatio("rgb(0, 0, 0)", "rgb(255, 255, 255)")
	if !ok {
		t.Fatal("expected ok")
	}
	if ratio < 20 {
		t.Fatalf("ratio = %v want ~21", ratio)
	}
}

func TestWCAGContrast_translucentBackgroundIsUnavailable(t *testing.T) {
	for _, bg := range []string{"rgba(0, 0, 0, 0)", "rgb(255 255 255 / 50%)", "#ffffff80", "transparent"} {
		if ratio, ok := wcagContrastRatio("rgb(0, 0, 0)", bg); ok {
			t.Fatalf("background %q: ratio %v, want unavailable", bg, ratio)
		}
	}
}

func TestWCAGContrast_channelsAreByteScaled(t *testing.T) {
	ratio, ok := wcagContrastRatio("rgb(1, 1, 1)", "rgb(255 255 255 / 1)")
	if !ok {
		t.Fatal("expected ok")
	}
	if ratio < 20 {
		t.Fatalf("near-black on white ratio = %v, want ~21", ratio)
	}
	if _, ok := wcagContrastRatio("#000f", "#fff"); !ok {
		t.Fatal("opaque short hex with alpha should parse")
	}
}

func TestMarshalMeasureReportPreservesLiteralHTML(t *testing.T) {
	raw, err := MarshalMeasureReport(MeasureResult{
		Units:   "px",
		Caption: `node <div> & Vec<u8>`,
	})
	testutil.FailErr(t, "MarshalMeasureReport failed", err)
	out := string(raw)
	for _, bad := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(out, bad) {
			t.Fatalf("measure report HTML-escaped %q: %s", bad, out)
		}
	}
	if !strings.Contains(out, "<div>") || !strings.Contains(out, "Vec<u8>") || !strings.Contains(out, " & ") {
		t.Fatalf("want literal HTML chars: %s", out)
	}
}
