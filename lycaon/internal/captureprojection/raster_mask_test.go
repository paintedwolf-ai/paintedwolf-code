package captureprojection

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func whitePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.White)
		}
	}
	var out bytes.Buffer
	testutil.FailErr(t, "encode raster fixture", png.Encode(&out, img))
	return out.Bytes()
}

func TestAScreenedMaskIsReusedAcrossFramesAndUnionsWithItsNeighbour(t *testing.T) {
	p := testProjector(t, nil)
	text := "total " + testSecret
	early, err := p.ScreenRegions(t.Context(), Scope{}, 320, 60, []Region{{Text: text, Spans: horizontalRuneSpans(text, 10, 10, 7, 18)}}, true)
	testutil.FailErr(t, "screen early sample", err)
	late, err := p.ScreenRegions(t.Context(), Scope{}, 320, 60, []Region{{Text: text, Spans: horizontalRuneSpans(text, 10, 34, 7, 18)}}, true)
	testutil.FailErr(t, "screen late sample", err)
	clean, err := p.ScreenRegions(t.Context(), Scope{}, 320, 60, []Region{{Text: "nothing here", Spans: horizontalRuneSpans("nothing here", 10, 10, 7, 18)}}, true)
	testutil.FailErr(t, "screen clean sample", err)
	if early.Empty() || late.Empty() || !clean.Empty() {
		t.Fatalf("masks: early empty=%v late empty=%v clean empty=%v", early.Empty(), late.Empty(), clean.Empty())
	}
	raw := whitePNG(t, 320, 60)
	untouched, err := clean.Apply("image/png", raw)
	testutil.FailErr(t, "apply clean mask", err)
	if !bytes.Equal(untouched, raw) {
		t.Fatal("a mask with nothing to hide re-encoded the frame")
	}
	both := early.Union(late)
	if both.Metadata.RedactedCount != 1 || !both.Metadata.StructuredCoverage {
		t.Fatalf("union metadata = %+v", both.Metadata)
	}
	masked, err := both.Apply("image/png", raw)
	testutil.FailErr(t, "apply union mask", err)
	img, err := png.Decode(bytes.NewReader(masked))
	testutil.FailErr(t, "decode masked frame", err)
	secretX := 10 + len([]rune("total "))*7 + 3
	for _, y := range []int{18, 42} {
		if got := color.RGBAModel.Convert(img.At(secretX, y)).(color.RGBA); got == (color.RGBA{255, 255, 255, 255}) {
			t.Fatalf("text painted at y=%d was not covered by the union", y)
		}
	}
	incomplete := early.Union(RasterMask{Metadata: Metadata{StructuredCoverage: false}})
	if incomplete.Metadata.StructuredCoverage {
		t.Fatal("a union with an unstructured sample claimed structured coverage")
	}
}
