package contactsheet

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func frame(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	return img
}

func TestComposeFitsTheGridInsideTheEdgeBudget(t *testing.T) {
	cells := make([]Cell, 12)
	for i := range cells {
		cells[i] = Cell{Image: frame(1280, 800), Label: "1.25s · after click the long button label that must be elided"}
	}
	raw, err := Compose(cells, Options{MaxEdge: 2048, Columns: 4})
	testutil.FailErr(t, "Compose failed", err)
	img, err := png.Decode(bytes.NewReader(raw))
	testutil.FailErr(t, "png.Decode failed", err)
	b := img.Bounds()
	if b.Dx() > 2048 || b.Dy() > 2048 || b.Dx() < 1500 {
		t.Fatalf("sheet is %dx%d", b.Dx(), b.Dy())
	}
}

func TestComposeNeverEnlargesSmallFrames(t *testing.T) {
	raw, err := Compose([]Cell{{Image: frame(200, 100), Label: "0.00s · start"}}, Options{MaxEdge: 2048, Columns: 4})
	testutil.FailErr(t, "Compose failed", err)
	img, _ := png.Decode(bytes.NewReader(raw))
	if img.Bounds().Dx() != 200+2*gutter {
		t.Fatalf("single small frame sheet width = %d", img.Bounds().Dx())
	}
	if _, err := Compose(nil, Options{MaxEdge: 2048, Columns: 4}); err == nil {
		t.Fatal("an empty sheet was composed")
	}
}

func TestClockLabelReadsMinutesSecondsAndTenths(t *testing.T) {
	for ms, want := range map[float64]string{0: "0:00.0", 61_250: "1:01.3", 599_990: "10:00.0"} {
		if got := ClockLabel(ms); got != want {
			t.Fatalf("ClockLabel(%v) = %q, want %q", ms, got, want)
		}
	}
}
