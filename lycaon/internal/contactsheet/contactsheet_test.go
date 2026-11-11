package contactsheet

import (
	"archive/zip"
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

func TestFilmstripPosterComposesFramesFromZip(t *testing.T) {
	var frameBuf bytes.Buffer
	testutil.FailErr(t, "png.Encode", png.Encode(&frameBuf, frame(4, 4)))
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"000.png", frameBuf.Bytes()},
		{"001.png", frameBuf.Bytes()},
		{"manifest.json", []byte(`{"version":1,"frames":[{"index":0,"file":"000.png","caption":"initial"},{"index":1,"file":"001.png","caption":"click #next"}]}`)},
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: entry.name, Method: zip.Store})
		testutil.FailErr(t, "CreateHeader", err)
		_, err = w.Write(entry.data)
		testutil.FailErr(t, "Write", err)
	}
	testutil.FailErr(t, "Close", zw.Close())

	sheet, err := FilmstripPoster(zbuf.Bytes(), 0)
	testutil.FailErr(t, "FilmstripPoster", err)
	cfg, err := png.DecodeConfig(bytes.NewReader(sheet))
	testutil.FailErr(t, "DecodeConfig", err)
	if cfg.Width <= 0 || cfg.Height <= 0 {
		t.Fatalf("unexpected sheet dimensions: %+v", cfg)
	}
	if got := FilmstripFrameLabel(0, ""); got != "#1" {
		t.Fatalf("FilmstripFrameLabel(0, empty) = %q, want #1", got)
	}
	if got := FilmstripFrameLabel(1, "click #next"); got != "#2 · click #next" {
		t.Fatalf("FilmstripFrameLabel(1, caption) = %q", got)
	}

	// Pre-composed poster.png fast path.
	var pbuf bytes.Buffer
	pzw := zip.NewWriter(&pbuf)
	pw, err := pzw.CreateHeader(&zip.FileHeader{Name: "poster.png", Method: zip.Store})
	testutil.FailErr(t, "CreateHeader poster", err)
	_, err = pw.Write(sheet)
	testutil.FailErr(t, "Write poster", err)
	testutil.FailErr(t, "Close poster zip", pzw.Close())
	fastSheet, err := FilmstripPoster(pbuf.Bytes(), 2048)
	testutil.FailErr(t, "FilmstripPoster fast path", err)
	if !bytes.Equal(fastSheet, sheet) {
		t.Fatal("expected pre-composed poster.png bytes")
	}

	// Error cases.
	if _, err := FilmstripPoster([]byte("not-a-zip"), 2048); err == nil {
		t.Fatal("expected error for invalid zip")
	}
	for _, entries := range [][]struct {
		name string
		data []byte
	}{
		{},
		{{"manifest.json", []byte(`{`)}},
		{{"manifest.json", []byte(`{"version":1,"frames":[]}`)}},
		{{"manifest.json", []byte(`{"version":1,"frames":[{"index":0,"file":"000.png"}]}`)}},
		{{"000.png", []byte("corrupt")}, {"manifest.json", []byte(`{"version":1,"frames":[{"index":0,"file":"000.png"}]}`)}},
	} {
		var ebuf bytes.Buffer
		ezw := zip.NewWriter(&ebuf)
		for _, e := range entries {
			ew, _ := ezw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store})
			_, _ = ew.Write(e.data)
		}
		_ = ezw.Close()
		if _, err := FilmstripPoster(ebuf.Bytes(), 2048); err == nil {
			t.Fatalf("expected error for malformed filmstrip entries %+v", entries)
		}
	}
}
