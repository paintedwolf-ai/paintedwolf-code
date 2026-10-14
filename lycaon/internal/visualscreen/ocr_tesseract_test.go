package visualscreen

import (
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/ocrengine"
	"github.com/lycaon/lycaon/internal/testutil"
)

const tsvHeader = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n"

func TestParseTesseractTSVGroupsWordsIntoLines(t *testing.T) {
	tsv := tsvHeader +
		"1\t1\t0\t0\t0\t0\t0\t0\t200\t100\t-1\t\n" +
		"2\t1\t1\t0\t0\t0\t10\t10\t180\t80\t-1\t\n" +
		"4\t1\t1\t1\t1\t0\t10\t10\t180\t20\t-1\t\n" +
		"5\t1\t1\t1\t1\t1\t10\t10\t40\t20\t96.5\tPublic\n" +
		"5\t1\t1\t1\t1\t2\t60\t12\t50\t16\t90.1\theading\n" +
		"5\t1\t1\t1\t1\t3\t120\t14\t20\t10\t-1\t \n" +
		"5\t1\t1\t1\t2\t1\t10\t50\t100\t20\t80\tkey\n" +
		"5\t1\t2\t1\t1\t1\t10\t80\t60\t10\t70\tfooter\n"
	spans, err := parseTesseractTSV([]byte(tsv), 200, 100)
	if err != nil {
		testutil.FailErr(t, "parse", err)
	}
	if len(spans) != 3 {
		t.Fatalf("spans = %+v", spans)
	}
	first := spans[0]
	if first.Text != "Public heading" || math.Abs(first.Confidence-0.933) > 0.001 {
		t.Fatalf("first span = %+v", first)
	}
	want := [4]float64{10.0 / 200, 10.0 / 100, 100.0 / 200, 20.0 / 100}
	for i := range want {
		if math.Abs(first.BoundingBox[i]-want[i]) > 1e-9 {
			t.Fatalf("first box = %v, want %v", first.BoundingBox, want)
		}
	}
	if spans[1].Text != "key" || spans[2].Text != "footer" {
		t.Fatalf("later spans = %+v", spans[1:])
	}
}

func TestParseTesseractTSVRejectsMalformedOutput(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"wrong header": "a\tb\n",
		"short row":    tsvHeader + "5\t1\n",
		"bad geometry": tsvHeader + "5\t1\t1\t1\t1\t1\tx\t10\t40\t20\t96\tword\n",
		"bad conf":     tsvHeader + "5\t1\t1\t1\t1\t1\t1\t10\t40\t20\thigh\tword\n",
	}
	for name, tsv := range cases {
		if _, err := parseTesseractTSV([]byte(tsv), 10, 10); err == nil {
			t.Fatalf("%s: parsed without error", name)
		}
	}
	if _, err := parseTesseractTSV([]byte(tsvHeader), 0, 10); err == nil {
		t.Fatal("zero raster size parsed")
	}
	spans, err := parseTesseractTSV([]byte(tsvHeader), 10, 10)
	if err != nil || len(spans) != 0 {
		t.Fatalf("header only: spans=%v err=%v", spans, err)
	}
}

func TestTesseractScaleEnlargesSmallRasters(t *testing.T) {
	cases := []struct{ w, h, want int }{
		{4, 4, 4}, {300, 100, 4}, {601, 10, 2}, {1199, 1199, 2}, {1200, 10, 1}, {4096, 4096, 1}, {0, 0, 1},
	}
	for _, tc := range cases {
		if got := tesseractScale(image.Rect(0, 0, tc.w, tc.h)); got != tc.want {
			t.Fatalf("scale(%dx%d) = %d, want %d", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestEncodePGMScalesAndFlattensAlpha(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.NRGBA{A: 255})       // black
	img.Set(1, 0, color.NRGBA{R: 255, A: 0}) // transparent reads as paper
	data, w, h := encodePGM(img, 3)
	if w != 6 || h != 3 {
		t.Fatalf("size = %dx%d", w, h)
	}
	header := "P5\n6 3\n255\n"
	if !strings.HasPrefix(string(data), header) || len(data) != len(header)+18 {
		t.Fatalf("pgm = %q", data)
	}
	pixels := data[len(header):]
	for y := 0; y < 3; y++ {
		row := pixels[y*6 : y*6+6]
		if string(row) != "\x00\x00\x00\xff\xff\xff" {
			t.Fatalf("row %d = %v", y, row)
		}
	}
	if g := grayOf(color.RGBA{R: 255, G: 255, B: 255, A: 255}); g != 255 {
		t.Fatalf("white = %d", g)
	}
	if g := grayOf(color.RGBA{R: 128, G: 128, B: 128, A: 255}); g != 128 {
		t.Fatalf("mid gray = %d", g)
	}
}

// scriptEngine stands in a shell script as the engine so the process seam runs
// for real without a recognizer.
func scriptEngine(t *testing.T, script string) ocrengine.Engine {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script engines need sh")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "tesseract")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	return ocrengine.Engine{Root: root, Binary: bin, TessdataDir: filepath.Join(root, "tessdata"), Version: "script"}
}

func resolving(engine ocrengine.Engine) func() (ocrengine.Engine, error) {
	return func() (ocrengine.Engine, error) { return engine, nil }
}

func TestTesseractOCRReportsUnavailableEngine(t *testing.T) {
	unavailable := &ocrengine.UnavailableError{Reason: ocrengine.ReasonDigestMismatch, Path: "bin/tesseract"}
	ocr := newTesseractOCR(func() (ocrengine.Engine, error) { return ocrengine.Engine{}, unavailable })
	_, err := ocr.RecognizeText(context.Background(), "image/png", whitePNG(t, 8, 8))
	if !errors.Is(err, ErrOCRUnavailable) || !errors.Is(err, unavailable) {
		t.Fatalf("err = %v", err)
	}
	spans, err := ocr.RecognizeText(context.Background(), "image/png", nil)
	if err != nil || spans != nil {
		t.Fatalf("empty input: %v %v", spans, err)
	}
}

func TestTesseractOCRReadsEngineOutput(t *testing.T) {
	engine := scriptEngine(t, `
[ "$1" = stdin ] && [ "$2" = stdout ] || exit 3
[ -n "$TESSDATA_PREFIX" ] || exit 4
head -c 2 | grep -q P5 || exit 5
cat >/dev/null
printf 'level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n'
printf '5\t1\t1\t1\t1\t1\t0\t0\t16\t8\t90\thello\n'
`)
	ocr := newTesseractOCR(resolving(engine))
	spans, err := ocr.RecognizeText(context.Background(), "image/png", whitePNG(t, 8, 4))
	if err != nil {
		testutil.FailErr(t, "recognize", err)
	}
	// The 8x4 raster is scaled by 4, so a 16x8 word covers half of each edge.
	if len(spans) != 1 || spans[0].Text != "hello" || spans[0].BoundingBox != [4]float64{0, 0, 0.5, 0.5} || spans[0].Confidence != 0.9 {
		t.Fatalf("spans = %+v", spans)
	}
}

func TestTesseractOCRFailsOnEngineExitAndGarbage(t *testing.T) {
	failing := newTesseractOCR(resolving(scriptEngine(t, "echo 'Error opening data file' >&2\nexit 1\n")))
	_, err := failing.RecognizeText(context.Background(), "image/png", whitePNG(t, 8, 8))
	if err == nil || errors.Is(err, ErrOCRUnavailable) || !strings.Contains(err.Error(), "exited 1") || !strings.Contains(err.Error(), "Error opening data file") {
		t.Fatalf("exit err = %v", err)
	}
	garbage := newTesseractOCR(resolving(scriptEngine(t, "echo not tsv\n")))
	if _, err := garbage.RecognizeText(context.Background(), "image/png", whitePNG(t, 8, 8)); !errors.Is(err, errTSVHeader) {
		t.Fatalf("garbage err = %v", err)
	}
	undecodable := newTesseractOCR(resolving(scriptEngine(t, "exit 0\n")))
	if _, err := undecodable.RecognizeText(context.Background(), "image/png", []byte("\x89PNG\r\n\x1a\nnot a png")); err == nil || errors.Is(err, ErrOCRUnavailable) {
		t.Fatalf("undecodable err = %v", err)
	}
}

func TestTesseractOCRBoundsTimeAndOutput(t *testing.T) {
	slow := newTesseractOCR(resolving(scriptEngine(t, "sleep 5\n")))
	slow.timeout = 200 * time.Millisecond
	started := time.Now()
	_, err := slow.RecognizeText(context.Background(), "image/png", whitePNG(t, 8, 8))
	if !errors.Is(err, execpkg.ErrTimeout) || errors.Is(err, ErrOCRUnavailable) {
		t.Fatalf("timeout err = %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("the engine outlived its deadline")
	}
	flood := newTesseractOCR(resolving(scriptEngine(t, "yes 'level\tx' | head -c 100000\n")))
	flood.maxOutputBytes = 4096
	if _, err := flood.RecognizeText(context.Background(), "image/png", whitePNG(t, 8, 8)); !errors.Is(err, execpkg.ErrOutputTruncated) {
		t.Fatalf("flood err = %v", err)
	}
}

func TestLastLine(t *testing.T) {
	if got := lastLine([]byte("warning\nfatal: boom\n\n")); got != "fatal: boom" {
		t.Fatalf("lastLine = %q", got)
	}
	if got := lastLine(nil); got != "" {
		t.Fatalf("lastLine(nil) = %q", got)
	}
}
