package page

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestEffectiveRenderPatchPreservesCommittedRevisionAndPresentation(t *testing.T) {
	store := renderhandle.NewStore()
	saved, err := store.Put("session", &renderhandle.RenderHandle{ID: "view", Markup: "<p>old</p>", Mime: "text/html", Theme: "dark", Fonts: []string{"fixture"}, Caption: "caption", Viewport: renderhandle.ViewportConfig{Width: 320, Height: 180, Scale: 2, Fit: "content"}}, 0)
	if err != nil {
		testutil.FailErr(t, "save initial render revision", err)
	}
	eff, err := resolveEffectiveRender(renderViewArgs{Handle: "view", OldString: "old", NewString: "new"}, "session", store)
	if err != nil {
		testutil.FailErr(t, "prepare render patch", err)
	}
	if eff.markup != "<p>new</p>" || eff.base != saved.Revision || eff.mime != "text/html" || eff.theme != "dark" || eff.caption != "caption" || eff.width != 320 || eff.height != 180 || eff.scale != 2 || len(eff.fonts) != 1 {
		t.Fatalf("patch lost presentation or revision: %+v", eff)
	}
	current, _ := store.Get("session", "view")
	if current.Markup != saved.Markup || current.Revision != saved.Revision {
		t.Fatal("preparing render committed patch before raster succeeded")
	}
	replay, err := resolveEffectiveRender(renderViewArgs{Handle: "view"}, "session", store)
	if err != nil || replay.markup != saved.Markup || replay.base != saved.Revision {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	for _, tc := range []struct{ session, old, code string }{{"other", "old", "RENDER_HANDLE_NOT_FOUND"}, {"session", "missing", "RENDER_PATCH_NOT_FOUND"}} {
		_, err := resolveEffectiveRender(renderViewArgs{Handle: "view", OldString: tc.old, NewString: "new"}, tc.session, store)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != tc.code {
			t.Fatalf("patch refusal=%v want=%s", err, tc.code)
		}
	}
	if _, err := store.Put("session", &renderhandle.RenderHandle{ID: "ambiguous", Markup: "old old", Mime: "text/html"}, 0); err != nil {
		testutil.FailErr(t, "save ambiguous render fixture", err)
	}
	_, err = resolveEffectiveRender(renderViewArgs{Handle: "ambiguous", OldString: "old", NewString: "new"}, "session", store)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "RENDER_PATCH_AMBIGUOUS" || reject.Data["match_count"] != "2" {
		t.Fatalf("ambiguous refusal=%v", err)
	}
	current, _ = store.Get("session", "ambiguous")
	if current.Markup != "old old" {
		t.Fatal("ambiguous patch changed markup")
	}
}

func TestRenderAdmissionRejectsMissingSourceAndMIMEWithoutChangingSavedView(t *testing.T) {
	store := renderhandle.NewStore()
	if _, err := store.Put("session", &renderhandle.RenderHandle{ID: "view", Markup: "<p>saved</p>", Mime: "html"}, 0); err != nil {
		testutil.FailErr(t, "save admission render fixture", err)
	}
	for _, item := range []struct {
		in   renderViewArgs
		code string
	}{
		{renderViewArgs{}, "TOOL_ARGS_INVALID"}, {renderViewArgs{Markup: "<p>new</p>"}, "TOOL_ARGS_INVALID"}, {renderViewArgs{Handle: "missing"}, "RENDER_HANDLE_NOT_FOUND"}, {renderViewArgs{Handle: "view", Markup: "<p>new</p>"}, "TOOL_ARGS_INVALID"},
	} {
		_, err := resolveEffectiveRender(item.in, "session", store)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != item.code {
			t.Fatalf("render admission=%v want=%s", err, item.code)
		}
		current, _ := store.Get("session", "view")
		if current.Markup != "<p>saved</p>" {
			t.Fatal("invalid render changed saved markup")
		}
	}
}
