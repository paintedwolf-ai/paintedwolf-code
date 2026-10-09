package native

import (
	"bytes"
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/page"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/pkg/api"
)

func createTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	testutil.FailErr(t, "encode png", png.Encode(&buf, img))
	return buf.Bytes()
}

func TestViewImageTool(t *testing.T) {
	tempDir := t.TempDir()
	reg := tools.NewDefaultRegistry()
	handleStore := renderhandle.NewStore()
	testutil.FailErr(t, "RegisterViewImageTool", RegisterViewImageTool(reg, page.ViewImageDeps{
		Boundary:    nativefixture.Boundary(t),
		Raster:      testRasterizer(t),
		HandleStore: handleStore,
	}))

	t.Run("valid png returns metadata and visual capture", func(t *testing.T) {
		pngPath := filepath.Join(tempDir, "test.png")
		testutil.FailErr(t, "write png", os.WriteFile(pngPath, createTestPNG(t, 20, 30), 0o644))

		tctx := nativefixture.Context(tempDir)
		tctx.Out = &tools.ToolInvocationOut{}
		out, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "test.png"}, tctx)
		testutil.FailErr(t, "run view_image", err)

		if tctx.Out.Visual == nil {
			t.Fatal("expected VisualCapture in invocation out")
		}
		if !tctx.Out.Visual.Perceive {
			t.Fatal("expected VisualCapture.Perceive to be true")
		}
		if tctx.Out.Visual.Mime != "image/png" {
			t.Fatalf("mime = %q want image/png", tctx.Out.Visual.Mime)
		}
		if !strings.Contains(out, `"width":20`) || !strings.Contains(out, `"height":30`) {
			t.Fatalf("unexpected output: %s", out)
		}
	})

	t.Run("unsupported format rejects IMAGE_FORMAT_UNSUPPORTED", func(t *testing.T) {
		txtPath := filepath.Join(tempDir, "test.txt")
		testutil.FailErr(t, "write txt", os.WriteFile(txtPath, []byte("hello"), 0o644))

		tctx := nativefixture.Context(tempDir)
		tctx.Out = &tools.ToolInvocationOut{}
		_, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "test.txt"}, tctx)
		if err == nil {
			t.Fatal("expected reject")
		}
		var rej *toolrejection.ToolReject
		if !errors.As(err, &rej) || rej.Code != "IMAGE_FORMAT_UNSUPPORTED" {
			t.Fatalf("got %#v want IMAGE_FORMAT_UNSUPPORTED", err)
		}
	})

	t.Run("missing file rejects IMAGE_NOT_FOUND", func(t *testing.T) {
		tctx := nativefixture.Context(tempDir)
		tctx.Out = &tools.ToolInvocationOut{}
		_, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "missing.png"}, tctx)
		if err == nil {
			t.Fatal("expected reject")
		}
		var rej *toolrejection.ToolReject
		if !errors.As(err, &rej) || rej.Code != "IMAGE_NOT_FOUND" {
			t.Fatalf("got %#v want IMAGE_NOT_FOUND", err)
		}
	})

	t.Run("directory rejects IMAGE_IS_DIRECTORY", func(t *testing.T) {
		subDir := filepath.Join(tempDir, "subdir.png")
		testutil.FailErr(t, "mkdir", os.Mkdir(subDir, 0o755))

		tctx := nativefixture.Context(tempDir)
		tctx.Out = &tools.ToolInvocationOut{}
		_, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "subdir.png"}, tctx)
		if err == nil {
			t.Fatal("expected reject")
		}
		var rej *toolrejection.ToolReject
		if !errors.As(err, &rej) || rej.Code != "IMAGE_IS_DIRECTORY" {
			t.Fatalf("got %#v want IMAGE_IS_DIRECTORY", err)
		}
	})

	t.Run("corrupted image rejects IMAGE_CORRUPTED", func(t *testing.T) {
		badPath := filepath.Join(tempDir, "bad.png")
		testutil.FailErr(t, "write bad png", os.WriteFile(badPath, []byte("not a png file"), 0o644))

		tctx := nativefixture.Context(tempDir)
		tctx.Out = &tools.ToolInvocationOut{}
		_, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "bad.png"}, tctx)
		if err == nil {
			t.Fatal("expected reject")
		}
		var rej *toolrejection.ToolReject
		if !errors.As(err, &rej) || rej.Code != "IMAGE_CORRUPTED" {
			t.Fatalf("got %#v want IMAGE_CORRUPTED", err)
		}
	})

	t.Run("inspect in-memory handle succeeds", func(t *testing.T) {
		tctx := nativefixture.Context(tempDir)
		tctx.Out = &tools.ToolInvocationOut{}
		_, err := handleStore.Put(tctx.SessionID, &renderhandle.RenderHandle{
			ID:      "dashboard-hero",
			Markup:  `<svg></svg>`,
			Mime:    "svg",
			Bytes:   createTestPNG(t, 64, 48),
			Canvas:  browser.RenderCanvas{Width: 64, Height: 48},
			Caption: "Hero banner",
		}, 0)
		testutil.FailErr(t, "seed handle", err)
		out, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"handle": "dashboard-hero"}, tctx)
		testutil.FailErr(t, "run view_image on handle", err)

		if !strings.Contains(out, `"handle":"dashboard-hero"`) || !strings.Contains(out, `"width":64`) {
			t.Fatalf("unexpected view_image handle output: %s", out)
		}
		if tctx.Out.Visual == nil || !tctx.Out.Visual.Perceive {
			t.Fatalf("expected perceived visual capture, got %+v", tctx.Out.Visual)
		}
	})

	t.Run("nonexistent handle rejects RENDER_HANDLE_NOT_FOUND", func(t *testing.T) {
		tctx := nativefixture.Context(tempDir)
		tctx.Out = &tools.ToolInvocationOut{}
		_, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"handle": "missing-handle"}, tctx)
		if err == nil {
			t.Fatal("expected reject")
		}
		var rej *toolrejection.ToolReject
		if !errors.As(err, &rej) || rej.Code != "RENDER_HANDLE_NOT_FOUND" {
			t.Fatalf("got %#v want RENDER_HANDLE_NOT_FOUND", err)
		}
	})

	t.Run("svg contains extracted text in receipt", func(t *testing.T) {
		browsertest.SkipIfNoBrowser(t)
		svgPath := filepath.Join(tempDir, "diagram.svg")
		testutil.FailErr(t, "write svg", os.WriteFile(svgPath, []byte(`<svg width="100" height="100"><text>Database Engine</text></svg>`), 0o644))

		tctx := nativefixture.Context(tempDir)
		tctx.Out = &tools.ToolInvocationOut{}
		out, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "diagram.svg"}, tctx)
		testutil.FailErr(t, "run view_image", err)

		if !strings.Contains(out, `"text":"Database Engine"`) {
			t.Fatalf("expected extracted text in receipt, got: %s", out)
		}
	})
}

func TestViewImageToolSecretScreening(t *testing.T) {
	tempDir := t.TempDir()
	secretSVG := filepath.Join(tempDir, "secret.svg")
	testutil.FailErr(t, "write svg", os.WriteFile(secretSVG, []byte(`<svg width="100" height="100"><text>AKIAQYJK5TXV4NZR7SGB</text></svg>`), 0o644))

	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	secretGate := visualscreen.NewGate(visualscreen.NewScanner(nil), matcher, func(ctx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
	})

	screenReg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterViewImageTool", RegisterViewImageTool(screenReg, page.ViewImageDeps{
		Boundary:    nativefixture.Boundary(t),
		Raster:      testRasterizer(t),
		HandleStore: renderhandle.NewStore(),
		Screen:      secretGate,
	}))

	tctx := nativefixture.Context(tempDir)
	tctx.Out = &tools.ToolInvocationOut{}
	_, err = screenReg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "secret.svg"}, tctx)
	if err == nil {
		t.Fatal("expected secret withheld reject")
	}
	var rej *toolrejection.ToolReject
	if !errors.As(err, &rej) || rej.Code != "IMAGE_SECRET_WITHHELD" {
		t.Fatalf("expected IMAGE_SECRET_WITHHELD, got: %#v", err)
	}
}

// A URL is not a view_image source: remote images go through fetch_url and
// its own approval tier.
func TestViewImageToolRejectsURLSource(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterViewImageTool", RegisterViewImageTool(reg, page.ViewImageDeps{
		Boundary:    nativefixture.Boundary(t),
		Raster:      testRasterizer(t),
		HandleStore: renderhandle.NewStore(),
	}))
	tctx := nativefixture.Context(t.TempDir())
	_, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"url": "https://example.com/mockup.png"}, tctx)
	var rej *toolrejection.ToolReject
	if !errors.As(err, &rej) || rej.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("err = %#v, want TOOL_ARGS_INVALID", err)
	}
}

// countingOCR reports a fixed outcome and counts recognitions.
type countingOCR struct {
	err   error
	calls int
}

func (o *countingOCR) RecognizeText(context.Context, string, []byte) ([]visualscreen.TextSpan, error) {
	o.calls++
	return nil, o.err
}

func TestViewImageRasterWithoutOCRAsksWithCoverageReason(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write png", os.WriteFile(filepath.Join(dir, "shot.png"), createTestPNG(t, 8, 8), 0o644))
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	var alerts []secretmatch.Alert
	ocr := &countingOCR{err: visualscreen.ErrOCRUnavailable}
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterViewImageTool", RegisterViewImageTool(reg, page.ViewImageDeps{
		Boundary: nativefixture.Boundary(t),
		Screen: visualscreen.NewGate(visualscreen.NewScanner(ocr), matcher, func(_ context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
			alerts = append(alerts, alert)
			return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
		}),
	}))
	tctx := nativefixture.Context(dir)
	tctx.Out = &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "shot.png"}, tctx)
	testutil.FailErr(t, "run view_image", err)
	if len(alerts) != 1 || alerts[0].ScreeningGap != secretmatch.GapOCRUnavailable || alerts[0].SourcePath != "shot.png" {
		t.Fatalf("alerts = %+v", alerts)
	}
	if !strings.Contains(out, `"screening_gap":"ocr_unavailable"`) {
		t.Fatalf("result does not report the coverage gap: %s", out)
	}
	if tctx.Out.Visual == nil || !tctx.Out.Visual.Perceive {
		t.Fatalf("approved image not perceived: %+v", tctx.Out.Visual)
	}
}

func TestViewImageRejectsOversizedRasterBeforeOCR(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write png", os.WriteFile(filepath.Join(dir, "wide.png"), createTestPNG(t, 4200, 1), 0o644))
	ocr := &countingOCR{}
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterViewImageTool", RegisterViewImageTool(reg, page.ViewImageDeps{
		Boundary: nativefixture.Boundary(t),
		Screen:   visualscreen.NewGate(visualscreen.NewScanner(ocr), nil, nil),
	}))
	_, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "wide.png"}, nativefixture.Context(dir))
	var rej *toolrejection.ToolReject
	if !errors.As(err, &rej) || rej.Code != "IMAGE_DIMENSIONS_EXCEEDED" {
		t.Fatalf("err = %#v, want IMAGE_DIMENSIONS_EXCEEDED", err)
	}
	if ocr.calls != 0 {
		t.Fatalf("OCR ran %d times on an oversized raster", ocr.calls)
	}
}

func TestViewImageRefusesOversizedFileBeforeReading(t *testing.T) {
	dir := t.TempDir()
	limit := visual.MaxBytesForMime("image/png")
	path := filepath.Join(dir, "huge.png")
	testutil.FailErr(t, "write png header", os.WriteFile(path, createTestPNG(t, 1, 1), 0o644))
	testutil.FailErr(t, "extend png past the limit", os.Truncate(path, int64(limit)+1))
	ocr := &countingOCR{}
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterViewImageTool", RegisterViewImageTool(reg, page.ViewImageDeps{
		Boundary: nativefixture.Boundary(t),
		Screen:   visualscreen.NewGate(visualscreen.NewScanner(ocr), nil, nil),
	}))
	_, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"path": "huge.png"}, nativefixture.Context(dir))
	var rej *toolrejection.ToolReject
	if !errors.As(err, &rej) || rej.Code != "IMAGE_BYTES_EXCEEDED" {
		t.Fatalf("err = %#v, want IMAGE_BYTES_EXCEEDED", err)
	}
	if rej.Data["max_bytes"] != limit || rej.Data["bytes"] != int64(limit)+1 {
		t.Fatalf("reject data = %#v, want bytes %d max_bytes %d", rej.Data, int64(limit)+1, limit)
	}
	if ocr.calls != 0 {
		t.Fatalf("OCR ran %d times on an oversized file", ocr.calls)
	}
}

// Artifacts live under the tree root; a worker session resolves through it.
func TestViewImageHandleResolvesThroughRootSession(t *testing.T) {
	store := visual.NewMemoryStore()
	art, err := store.Put(context.Background(), "root-session", visual.Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture, Perceive: true},
		Bytes: createTestPNG(t, 10, 6),
	})
	testutil.FailErr(t, "put artifact", err)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterViewImageTool", RegisterViewImageTool(reg, page.ViewImageDeps{
		Boundary:    nativefixture.Boundary(t),
		VisualStore: store,
		RootSessionID: func(_ context.Context, sessionID string) string {
			if sessionID == "worker-session" {
				return "root-session"
			}
			return sessionID
		},
	}))
	tctx := nativefixture.Context(t.TempDir())
	tctx.SessionID = "worker-session"
	tctx.Out = &tools.ToolInvocationOut{}
	out, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"handle": art.ID}, tctx)
	testutil.FailErr(t, "view artifact from worker", err)
	if !strings.Contains(out, `"width":10`) || tctx.Out.Visual == nil {
		t.Fatalf("out = %s visual = %+v", out, tctx.Out.Visual)
	}
}

// Only an artifact recorded as not perceived is screened again.
func TestViewImageHandleScreensUnperceivedArtifacts(t *testing.T) {
	store := visual.NewMemoryStore()
	put := func(perceive bool) string {
		art, err := store.Put(context.Background(), "test-session", visual.Entry{
			Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceWorkspace, Perceive: perceive},
			Bytes: createTestPNG(t, 4, 4),
		})
		testutil.FailErr(t, "put artifact", err)
		return art.ID
	}
	perceived, unperceived := put(true), put(false)
	ocr := &countingOCR{err: visualscreen.ErrOCRUnavailable}
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	asks := 0
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterViewImageTool", RegisterViewImageTool(reg, page.ViewImageDeps{
		Boundary:    nativefixture.Boundary(t),
		VisualStore: store,
		Screen: visualscreen.NewGate(visualscreen.NewScanner(ocr), matcher, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
			asks++
			return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
		}),
	}))
	for _, id := range []string{perceived, unperceived} {
		tctx := nativefixture.Context(t.TempDir())
		tctx.Out = &tools.ToolInvocationOut{}
		_, err := reg.Run(context.Background(), page.ViewImageToolName, map[string]any{"handle": id}, tctx)
		testutil.FailErr(t, "view artifact", err)
	}
	if ocr.calls != 1 || asks != 1 {
		t.Fatalf("ocr calls = %d, asks = %d; want one screen for the unperceived artifact", ocr.calls, asks)
	}
}
