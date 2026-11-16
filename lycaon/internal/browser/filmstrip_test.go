package browser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPackUnpackFilmstripRoundTrip(t *testing.T) {
	frames := []CaptureFrame{
		{Index: 0, Caption: "initial", Bytes: []byte{0x89, 0x50, 0x4e, 0x47}, Mime: "image/png"},
		{Index: 1, Caption: "click #go", Bytes: []byte{0x89, 0x50, 0x4e, 0x48}, Mime: "image/png"},
	}
	raw, err := PackFilmstripZip(frames)
	testutil.FailErr(t, "PackFilmstripZip failed", err)
	got, err := unpackFilmstripZip(raw)
	testutil.FailErr(t, "UnpackFilmstripZip failed", err)
	if len(got) != 2 {
		t.Fatalf("frames=%d", len(got))
	}
	if got[0].Caption != "initial" || got[1].Caption != "click #go" {
		t.Fatalf("captions: %#v", got)
	}
	if string(got[1].Bytes) != string(frames[1].Bytes) {
		t.Fatal("png bytes mismatch")
	}
}

func TestFilmstripPosterRendersContactSheetFromValidPNGFrames(t *testing.T) {
	framePNG := func(w, h int, c color.RGBA) []byte {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				img.Set(x, y, c)
			}
		}
		var buf bytes.Buffer
		testutil.FailErr(t, "png.Encode", png.Encode(&buf, img))
		return buf.Bytes()
	}
	frames := []CaptureFrame{
		{Index: 0, Caption: "initial", Bytes: framePNG(64, 48, color.RGBA{R: 200, G: 40, B: 40, A: 255}), Mime: "image/png"},
		{Index: 1, Caption: "click #next", Bytes: framePNG(64, 48, color.RGBA{R: 40, G: 200, B: 40, A: 255}), Mime: "image/png"},
	}
	raw, err := PackFilmstripZip(frames)
	testutil.FailErr(t, "PackFilmstripZip", err)
	sheet, err := FilmstripPoster(raw)
	testutil.FailErr(t, "FilmstripPoster", err)
	cfg, err := png.DecodeConfig(bytes.NewReader(sheet))
	testutil.FailErr(t, "DecodeConfig", err)
	if cfg.Width <= 64 || cfg.Height <= 48 {
		t.Fatalf("expected multi-frame contact sheet dimensions > 64x48, got %dx%d", cfg.Width, cfg.Height)
	}
	if _, err := ComposeFilmstripSheet(nil); err == nil {
		t.Fatal("expected error for empty frames")
	}
}

func TestNormalizeCaptureMode(t *testing.T) {
	m, err := NormalizeCaptureMode("")
	if err != nil || m != CaptureModeScreenshot {
		t.Fatalf("empty: %q %v", m, err)
	}
	m, err = NormalizeCaptureMode("filmstrip")
	if err != nil || m != CaptureModeFilmstrip {
		t.Fatalf("filmstrip: %q %v", m, err)
	}
	_, err = NormalizeCaptureMode("clip")
	if err == nil {
		t.Fatal("expected reject")
	}
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "CAPTURE_MODE_INVALID" {
		t.Fatalf("got %#v", err)
	}
}

func TestCaptureFilmstripMultiStepForm(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "multi-step-form"),
		Mode:       CaptureModeFilmstrip,
		Wait:       "idle",
		Actions: []CaptureAction{
			{Type: "fill", ActionLocator: ActionLocator{Selector: "#email"}, Value: "a@b.c"},
			{Type: "click", ActionLocator: ActionLocator{Selector: "#next"}},
			{Type: "wait_for", ActionLocator: ActionLocator{Selector: "#code"}, TimeoutMS: 5000},
			{Type: "fill", ActionLocator: ActionLocator{Selector: "#code"}, Value: "42"},
			{Type: "click", ActionLocator: ActionLocator{Selector: "#finish"}},
			{Type: "wait_for", ActionLocator: ActionLocator{Text: "done:a@b.c:42"}, TimeoutMS: 5000},
		},
		Width:  400,
		Height: 300,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if out.Mime != FilmstripMime {
		t.Fatalf("mime %q", out.Mime)
	}
	// 1 initial + 6 actions = 7 frames
	if len(out.Frames) != 7 {
		t.Fatalf("frames=%d want 7 captions=%v", len(out.Frames), frameCaptions(out.Frames))
	}
	if out.Frames[0].Caption != "initial" {
		t.Fatalf("frame0 caption %q", out.Frames[0].Caption)
	}
	lastSnap, _ := json.Marshal(out.Frames[len(out.Frames)-1].Snapshot)
	if !strings.Contains(string(lastSnap), "done:a@b.c:42") {
		t.Fatalf("last frame missing done status: %s", lastSnap)
	}
	firstSnap, _ := json.Marshal(out.Frames[0].Snapshot)
	if strings.Contains(string(firstSnap), "done:") {
		t.Fatal("initial frame should not already be done")
	}
	unpacked, err := unpackFilmstripZip(out.Bytes)
	testutil.FailErr(t, "UnpackFilmstripZip failed", err)
	if len(unpacked) != len(out.Frames) {
		t.Fatalf("zip frames=%d text frames=%d", len(unpacked), len(out.Frames))
	}
}

func TestCaptureFilmstripPopulateAfterTick(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "populate-after-tick"),
		Mode:       CaptureModeFilmstrip,
		Wait:       "idle",
		Actions: []CaptureAction{
			{Type: "click", ActionLocator: ActionLocator{Selector: "#load"}},
			{Type: "wait_for", ActionLocator: ActionLocator{Text: "gamma"}, TimeoutMS: 5000},
		},
		Width:  400,
		Height: 300,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(out.Frames) != 3 {
		t.Fatalf("frames=%d want 3", len(out.Frames))
	}
	pre, _ := json.Marshal(out.Frames[0].Snapshot)
	post, _ := json.Marshal(out.Frames[len(out.Frames)-1].Snapshot)
	if strings.Contains(string(pre), "gamma") {
		t.Fatalf("pre-load frame should not list gamma: %s", pre)
	}
	if !strings.Contains(string(post), "gamma") {
		t.Fatalf("post frame missing gamma: %s", post)
	}
}

func TestCaptureFilmstripNoActionsDegradesToScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("browser capture")
	}
	browsertest.SkipIfNoBrowser(t)
	pool := newCaptureTestPool("")
	defer pool.Close()
	cap := &CapturePool{Pool: pool}
	out, err := cap.Capture(context.Background(), CaptureRequest{
		ProjectDir: fixtureDir(t, "clean"),
		Mode:       CaptureModeFilmstrip,
		Width:      400,
		Height:     300,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if out.Mime != "image/png" {
		t.Fatalf("mime %q want image/png (degrade)", out.Mime)
	}
	if len(out.Frames) != 0 {
		t.Fatalf("screenshot path must not emit frames slice: %d", len(out.Frames))
	}
}

func frameCaptions(frames []CaptureFrame) []string {
	out := make([]string, len(frames))
	for i, fr := range frames {
		out[i] = fr.Caption
	}
	return out
}

func unpackFilmstripZip(raw []byte) ([]CaptureFrame, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	var man filmstripManifest
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		if f.Name == "manifest.json" {
			if err := json.Unmarshal(b, &man); err != nil {
				return nil, err
			}
			continue
		}
		files[f.Name] = b
	}
	if len(man.Frames) == 0 {
		return nil, fmt.Errorf("filmstrip manifest missing frames")
	}
	out := make([]CaptureFrame, 0, len(man.Frames))
	for _, fr := range man.Frames {
		png := files[fr.File]
		if len(png) == 0 {
			return nil, fmt.Errorf("missing filmstrip file %q", fr.File)
		}
		out = append(out, CaptureFrame{
			Index: fr.Index, Caption: fr.Caption, Mime: "image/png", Bytes: png,
		})
	}
	return out, nil
}
