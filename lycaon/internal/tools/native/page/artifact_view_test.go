package page

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestImageArtifactUsesResolvedRootAndKeepsStoredRasterReference(t *testing.T) {
	store := visual.NewMemoryStore()
	artifact, err := store.Put(t.Context(), "root", visual.Entry{Meta: wire.VisualArtifact{Mime: "image/png", Source: wire.VisualArtifactSourceCapture, Caption: "fixture", Perceive: true}, Bytes: visual.TestPNG1x1Bytes()})
	if err != nil {
		testutil.FailErr(t, "store image artifact", err)
	}
	effect := &tools.ToolInvocationOut{}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "child"}, Effects: tools.InvocationEffects{Out: effect}}
	handler := ViewImageHandler(ViewImageDeps{VisualStore: store, RootSessionID: func(_ context.Context, id string) string {
		if id != "child" {
			t.Fatal("incorrect session lookup")
		}
		return "root"
	}})
	raw, err := handler(t.Context(), map[string]any{"handle": artifact.ID}, tc)
	if err != nil || !json.Valid([]byte(raw)) || effect.Visual == nil || effect.Visual.ArtifactID != artifact.ID || len(effect.Visual.Bytes) != 0 || effect.Visual.Caption != "fixture" {
		t.Fatalf("artifact reference lost: raw=%q err=%v visual=%+v", raw, err, effect.Visual)
	}
	effect.Visual = nil
	foreign := ViewImageHandler(ViewImageDeps{VisualStore: store})
	raw, err = foreign(t.Context(), map[string]any{"handle": artifact.ID}, tc)
	var reject *toolrejection.ToolReject
	if raw != "" || !errors.As(err, &reject) || reject.Code != "RENDER_HANDLE_NOT_FOUND" || effect.Visual != nil {
		t.Fatalf("foreign root exposed artifact: raw=%q err=%v", raw, err)
	}
}

func TestFilmstripArtifactHandleViewsContactSheetPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			img.Set(x, y, color.RGBA{R: 60, G: 120, B: 200, A: 255})
		}
	}
	var frameBuf bytes.Buffer
	testutil.FailErr(t, "png.Encode", png.Encode(&frameBuf, img))
	framePNG := frameBuf.Bytes()

	store := visual.NewMemoryStore()
	zipBytes, err := browser.PackFilmstripZip([]browser.CaptureFrame{
		{Index: 0, Caption: "initial", Bytes: framePNG, Mime: "image/png"},
		{Index: 1, Caption: "click #next", Bytes: framePNG, Mime: "image/png"},
	})
	testutil.FailErr(t, "PackFilmstripZip", err)
	artifact, err := store.Put(t.Context(), "root", visual.Entry{
		Meta: wire.VisualArtifact{
			Mime:     browser.FilmstripMime,
			Source:   wire.VisualArtifactSourceCapture,
			Caption:  "multi-step flow",
			Perceive: true,
		},
		Bytes: zipBytes,
	})
	testutil.FailErr(t, "store filmstrip artifact", err)

	effect := &tools.ToolInvocationOut{}
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "root"},
		Effects:  tools.InvocationEffects{Out: effect},
	}
	handler := ViewImageHandler(ViewImageDeps{VisualStore: store})
	raw, err := handler(t.Context(), map[string]any{"handle": artifact.ID}, tc)
	testutil.FailErr(t, "view_image filmstrip handle", err)

	var res viewImageResult
	testutil.FailErr(t, "unmarshal viewImageResult", json.Unmarshal([]byte(raw), &res))
	if res.Format != "png" || res.Width <= 1 || res.Height <= 1 {
		t.Fatalf("expected png contact sheet dimensions > 1x1, got %+v", res)
	}
	if effect.Visual == nil || !effect.Visual.Perceive || effect.Visual.ArtifactID != artifact.ID {
		t.Fatalf("expected perceived filmstrip visual reference, got %+v", effect.Visual)
	}

	// Unperceived filmstrip artifact and corrupt filmstrip/timeline artifacts.
	unperceived, err := store.Put(t.Context(), "root", visual.Entry{
		Meta:  wire.VisualArtifact{Mime: browser.FilmstripMime, Source: wire.VisualArtifactSourceCapture, Perceive: false},
		Bytes: zipBytes,
	})
	testutil.FailErr(t, "store unperceived filmstrip", err)
	effect.Visual = nil
	if _, err := handler(t.Context(), map[string]any{"handle": unperceived.ID}, tc); err != nil || effect.Visual == nil || !effect.Visual.Perceive || len(effect.Visual.Bytes) == 0 {
		t.Fatalf("expected unperceived filmstrip to emit contact-sheet bytes, err=%v visual=%+v", err, effect.Visual)
	}

	corruptFilm, err := store.Put(t.Context(), "root", visual.Entry{
		Meta:  wire.VisualArtifact{Mime: browser.FilmstripMime, Source: wire.VisualArtifactSourceCapture, Perceive: true},
		Bytes: []byte("corrupt-zip"),
	})
	testutil.FailErr(t, "store corrupt filmstrip", err)
	var reject *toolrejection.ToolReject
	if _, err := handler(t.Context(), map[string]any{"handle": corruptFilm.ID}, tc); !errors.As(err, &reject) || reject.Code != "IMAGE_CORRUPTED" {
		t.Fatalf("expected IMAGE_CORRUPTED for corrupt filmstrip, got %v", err)
	}
	if reason, _ := reject.Data["rejection_reason"].(string); reason == "" {
		t.Fatal("corrupt filmstrip lost its diagnostic reason")
	}

	tlZip, err := timelinearchive.Pack(timelinearchive.Manifest{Version: 1, Frames: []timelinearchive.Frame{{AtMS: 0}}}, [][]byte{framePNG}, framePNG)
	testutil.FailErr(t, "timelinearchive.Pack", err)
	tlArt, err := store.Put(t.Context(), "root", visual.Entry{
		Meta:  wire.VisualArtifact{Mime: timelinearchive.Mime, Source: wire.VisualArtifactSourceCapture, Perceive: true},
		Bytes: tlZip,
	})
	testutil.FailErr(t, "store timeline artifact", err)
	if _, err := handler(t.Context(), map[string]any{"handle": tlArt.ID}, tc); err != nil {
		t.Fatalf("expected timeline artifact handle to view poster, got %v", err)
	}
	corruptTL, err := store.Put(t.Context(), "root", visual.Entry{
		Meta:  wire.VisualArtifact{Mime: timelinearchive.Mime, Source: wire.VisualArtifactSourceCapture, Perceive: true},
		Bytes: []byte("corrupt-tl"),
	})
	testutil.FailErr(t, "store corrupt timeline", err)
	if _, err := handler(t.Context(), map[string]any{"handle": corruptTL.ID}, tc); !errors.As(err, &reject) || reject.Code != "IMAGE_CORRUPTED" {
		t.Fatalf("expected IMAGE_CORRUPTED for corrupt timeline, got %v", err)
	}
	if reason, _ := reject.Data["rejection_reason"].(string); reason == "" {
		t.Fatal("corrupt timeline lost its diagnostic reason")
	}
}
