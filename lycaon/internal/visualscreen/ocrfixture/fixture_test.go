package ocrfixture

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderPlacesLinesWhereItReports(t *testing.T) {
	img, lines, err := Render(Options{Lines: []string{"first line", "second"}})
	if err != nil {
		testutil.FailErr(t, "render", err)
	}
	if len(lines) != 2 || lines[0].Text != "first line" || lines[1].Text != "second" {
		t.Fatalf("lines = %+v", lines)
	}
	b := img.Bounds()
	for i, line := range lines {
		box := line.BoundingBox
		if box[0] < 0 || box[1] < 0 || box[0]+box[2] > 1.0001 || box[1]+box[3] > 1.0001 {
			t.Fatalf("line %d box %v leaves the raster", i, box)
		}
		if i > 0 && box[1] <= lines[i-1].BoundingBox[1] {
			t.Fatalf("line %d is not below line %d: %v %v", i, i-1, lines[i-1].BoundingBox, box)
		}
		// Ink lands inside the reported box and nowhere in the padding above it.
		x0, y0 := int(box[0]*float64(b.Dx())), int(box[1]*float64(b.Dy()))
		x1, y1 := int((box[0]+box[2])*float64(b.Dx())), int((box[1]+box[3])*float64(b.Dy()))
		if !hasInk(img, image.Rect(x0, y0, x1, y1)) {
			t.Fatalf("line %d box %v holds no ink", i, box)
		}
	}
	if hasInk(img, image.Rect(0, 0, b.Dx(), int(lines[0].BoundingBox[1]*float64(b.Dy()))-1)) {
		t.Fatal("padding above the first line holds ink")
	}
}

func TestRenderEmptyIsBlank(t *testing.T) {
	img, lines, err := Render(Options{})
	if err != nil {
		testutil.FailErr(t, "render", err)
	}
	if len(lines) != 0 || hasInk(img, img.Bounds()) {
		t.Fatalf("empty render has %d lines, ink=%v", len(lines), hasInk(img, img.Bounds()))
	}
}

func TestRotateMovesInkAndBoxesTogether(t *testing.T) {
	for turns := 1; turns <= 3; turns++ {
		img, lines, err := Render(Options{Lines: []string{"turned"}, QuarterTurns: turns})
		if err != nil {
			testutil.FailErr(t, "render", err)
		}
		b := img.Bounds()
		box := lines[0].BoundingBox
		rect := image.Rect(int(box[0]*float64(b.Dx())), int(box[1]*float64(b.Dy())),
			int(math.Ceil((box[0]+box[2])*float64(b.Dx()))), int(math.Ceil((box[1]+box[3])*float64(b.Dy()))))
		if !hasInk(img, rect) {
			t.Fatalf("%d turns: rotated box %v holds no ink", turns, box)
		}
		outside := image.NewRGBA(b)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if !(image.Point{X: x, Y: y}).In(rect) {
					outside.Set(x, y, img.At(x, y))
				} else {
					outside.Set(x, y, color.White)
				}
			}
		}
		if hasInk(outside, b) {
			t.Fatalf("%d turns: ink outside the rotated box %v", turns, box)
		}
	}
}

func TestRotateDimensions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.Set(0, 0, color.Black)
	got := Rotate(src, 1)
	if got.Bounds().Dx() != 2 || got.Bounds().Dy() != 3 {
		t.Fatalf("quarter turn bounds = %v", got.Bounds())
	}
	if r, _, _, _ := got.At(1, 0).RGBA(); r != 0 {
		t.Fatal("top-left pixel did not move to the top-right corner")
	}
	if Rotate(src, 4) != src {
		t.Fatal("four turns must return the source")
	}
}

func hasInk(img image.Image, rect image.Rectangle) bool {
	rect = rect.Intersect(img.Bounds())
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if r, g, b, _ := img.At(x, y).RGBA(); r < 0x8000 || g < 0x8000 || b < 0x8000 {
				return true
			}
		}
	}
	return false
}
