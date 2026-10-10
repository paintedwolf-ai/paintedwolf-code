package page

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
)

func TestVideoAdmissionDistinguishesMissingDirectoryEmptyOversizedAndUnsupportedFiles(t *testing.T) {
	root := t.TempDir()
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}}}
	for path, content := range map[string]string{"empty.mp4": "", "large.mp4": "12345", "text.txt": "not video"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "directory.mp4"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path, code string
		cap        int64
	}{{"missing.mp4", "VIDEO_NOT_FOUND", 100}, {"directory.mp4", "VIDEO_IS_DIRECTORY", 100}, {"empty.mp4", "VIDEO_FORMAT_UNSUPPORTED", 100}, {"large.mp4", "VIDEO_BYTES_EXCEEDED", 2}, {"text.txt", "VIDEO_FORMAT_UNSUPPORTED", 100}} {
		out, err := ViewVideoHandler(ViewVideoDeps{MaxBytes: item.cap})(t.Context(), map[string]any{"path": item.path}, tc)
		var reject *toolrejection.ToolReject
		if out != "" || !errors.As(err, &reject) || reject.Code != item.code || reject.Data["path"] != item.path {
			t.Fatalf("video %s out=%q err=%v", item.path, out, err)
		}
	}
}

func TestImageHandleViewRetainsCommittedRasterRevision(t *testing.T) {
	store := renderhandle.NewStore()
	saved, err := store.Put("session", &renderhandle.RenderHandle{ID: "view", Bytes: []byte("projected raster"), Mime: "text/html", Caption: "caption", Canvas: browser.RenderCanvas{Width: 320, Height: 180}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	effect := &tools.ToolInvocationOut{}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}, Effects: tools.InvocationEffects{Out: effect}}
	raw, err := ViewImageHandler(ViewImageDeps{HandleStore: store})(t.Context(), map[string]any{"handle": "view"}, tc)
	if err != nil || !json.Valid([]byte(raw)) {
		t.Fatalf("view raw=%q err=%v", raw, err)
	}
	if effect.Visual == nil || string(effect.Visual.Bytes) != "projected raster" || effect.Visual.Caption != "caption" || !effect.Visual.Perceive {
		t.Fatalf("lost committed raster=%+v", effect.Visual)
	}
	var response viewImageResult
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	if response.Width != 320 || response.Height != 180 || response.Revision != saved.Revision {
		t.Fatalf("lost image coordinates=%+v", response)
	}
	current, _ := store.Get("session", "view")
	if current.Revision != saved.Revision {
		t.Fatal("view changed render revision")
	}
}

func TestScreenedRasterNormalizationCannotPublishCorruptPixels(t *testing.T) {
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	effect := &tools.ToolInvocationOut{}
	tc := tools.ToolContext{Effects: tools.InvocationEffects{Out: effect}}
	raw, err := renderRasterView(tc, source.Bytes(), "fixture.png", screened{bytes: []byte("corrupt screen output"), mime: "image/png", perception: visualscreen.PerceptionOriginal})
	var reject *toolrejection.ToolReject
	if raw != "" || !errors.As(err, &reject) || reject.Code != "IMAGE_CORRUPTED" || effect.Visual != nil {
		t.Fatalf("corrupt projected raster raw=%q err=%v visual=%+v", raw, err, effect.Visual)
	}
	raw, err = renderRasterView(tc, source.Bytes(), "fixture.png", screened{bytes: source.Bytes(), mime: "image/png", perception: visualscreen.PerceptionOriginal})
	if err != nil || !json.Valid([]byte(raw)) || effect.Visual == nil || !effect.Visual.Projected || !effect.Visual.Perceive {
		t.Fatalf("valid raster raw=%q err=%v visual=%+v", raw, err, effect.Visual)
	}
}

func TestRasterProjectionSupportsAbsentInvocationEffects(t *testing.T) {
	raw, err := renderRasterView(tools.ToolContext{}, visual.TestPNG1x1Bytes(), "fixture.png", screened{bytes: visual.TestPNG1x1Bytes(), mime: "image/png", perception: visualscreen.PerceptionOriginal})
	if err != nil || !json.Valid([]byte(raw)) {
		t.Fatalf("optional effects raster raw=%q err=%v", raw, err)
	}
}

func TestPageCapacityRefusalPreservesSessionAndLivePageCoordinates(t *testing.T) {
	err := capacityReject(&pagesession.CapacityError{Limit: 2, IDs: []string{"first", "second"}}, "session")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "PAGE_CAP_REACHED" || reject.Data["session_id"] != "session" || reject.Data["max_pages"] != 2 {
		t.Fatalf("capacity refusal=%v", err)
	}
	ids, ok := reject.Data["live_pages"].([]string)
	if !ok || len(ids) != 2 || ids[0] != "first" || ids[1] != "second" {
		t.Fatalf("lost live pages=%v", reject.Data)
	}
}

func TestRenderRefusesUnsupportedMarkupWithoutCommittingHandle(t *testing.T) {
	store := renderhandle.NewStore()
	raw, err := RenderViewHandler(nil, nil, store)(t.Context(), map[string]any{"markup": "valid text", "mime": "unsupported", "handle": "new"}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	var reject *toolrejection.ToolReject
	if raw != "" || !errors.As(err, &reject) || reject.Code != "RENDER_MARKUP_INVALID" || len(store.List("session")) != 0 {
		t.Fatalf("markup refusal raw=%q err=%v", raw, err)
	}
}

func TestVideoSheetProjectionKeepsFrameCoordinatesAndRefusesCorruptPixels(t *testing.T) {
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	effect := &tools.ToolInvocationOut{}
	tc := tools.ToolContext{Effects: tools.InvocationEffects{Out: effect}}
	sheet := browser.VideoSheet{Info: browser.VideoInfo{Width: 320, Height: 180, DurationMS: 1000}, Frames: []float64{0, 1000}, Sheet: source.Bytes()}
	raw, err := renderVideoView(t.Context(), ViewVideoDeps{}, tc, viewVideoArgs{Path: "clip.mp4"}, "clip.mp4", "video/mp4", sheet)
	if err != nil {
		t.Fatal(err)
	}
	var result viewVideoResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Width != 320 || result.Height != 180 || result.DurationMS != 1000 || len(result.Frames) != 2 || result.Frames[1].AtMS != 1000 || result.Frames[0].Label == result.Frames[1].Label || effect.Visual == nil || !effect.Visual.Perceive || !effect.Visual.Projected {
		t.Fatalf("lost frame projection: result=%+v visual=%+v", result, effect.Visual)
	}
	raw, err = renderVideoView(t.Context(), ViewVideoDeps{}, tools.ToolContext{}, viewVideoArgs{Path: "clip.mp4"}, "clip.mp4", "video/mp4", sheet)
	if err != nil || !json.Valid([]byte(raw)) {
		t.Fatalf("optional effects video raw=%q err=%v", raw, err)
	}
	effect.Visual = nil
	sheet.Sheet = []byte("corrupt decoder pixels")
	raw, err = renderVideoView(t.Context(), ViewVideoDeps{}, tc, viewVideoArgs{Path: "clip.mp4"}, "clip.mp4", "video/mp4", sheet)
	if raw != "" || err == nil || effect.Visual != nil {
		t.Fatalf("corrupt sheet published: raw=%q err=%v visual=%+v", raw, err, effect.Visual)
	}
	original := &browserengine.RejectError{Code: "VIDEO_FORMAT_UNSUPPORTED", Data: map[string]any{"reason": "decode"}}
	mapped := videoReject(original, "clip.mp4")
	var reject *toolrejection.ToolReject
	if !errors.As(mapped, &reject) || reject.Code != original.Code || reject.Data["path"] != "clip.mp4" || reject.Data["reason"] != "decode" {
		t.Fatalf("decoder facts lost: %v", mapped)
	}
	if _, exists := original.Data["path"]; exists {
		t.Fatal("mapping mutated decoder facts")
	}
}
