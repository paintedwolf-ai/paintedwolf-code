package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/browserengine/browsertest"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/page"
)

func testRasterizer(t *testing.T) *browser.Rasterizer {
	t.Helper()
	budgets, err := browser.LoadRenderBudgets()
	testutil.FailErr(t, "LoadRenderBudgets", err)
	r := browser.NewRasterizer("", budgets)
	// Rendered pixels are screened before a model sees them; the inert matcher screens nothing.
	r.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	return r
}

func TestRenderViewToolRejectsMissingMarkup(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := renderhandle.NewStore()
	if err := RegisterRenderViewTool(reg, nil, testRasterizer(t), store); err != nil {
		testutil.FailErr(t, "RegisterRenderViewTool failed", err)
	}
	_, err := reg.Run(context.Background(), page.RenderViewToolName, map[string]any{"mime": "svg"}, tools.ToolContext{
		Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	})
	if err == nil {
		t.Fatal("expected reject")
	}
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("got %#v want TOOL_ARGS_INVALID", err)
	}
}

func TestRenderViewToolRejectsNonPngDest(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := renderhandle.NewStore()
	if err := RegisterRenderViewTool(reg, nil, testRasterizer(t), store); err != nil {
		testutil.FailErr(t, "RegisterRenderViewTool failed", err)
	}
	_, err := reg.Run(context.Background(), page.RenderViewToolName, map[string]any{
		"mime":   "svg",
		"markup": "<svg></svg>",
		"dest":   "assets/logo.jpg",
	}, tools.ToolContext{
		Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	})
	if err == nil {
		t.Fatal("expected reject")
	}
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("got %#v want TOOL_ARGS_INVALID", err)
	}
	if rej.Data["reason"] != "invalid_dest_extension" {
		t.Fatalf("reason: got %v want invalid_dest_extension", rej.Data["reason"])
	}
}

func TestRenderViewToolRejectsInvalidScale(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := renderhandle.NewStore()
	if err := RegisterRenderViewTool(reg, nil, testRasterizer(t), store); err != nil {
		testutil.FailErr(t, "RegisterRenderViewTool failed", err)
	}
	_, err := reg.Run(context.Background(), page.RenderViewToolName, map[string]any{
		"mime":   "svg",
		"markup": "<svg></svg>",
		"viewport": map[string]any{
			"scale": 10.0,
		},
	}, tools.ToolContext{
		Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	})
	if err == nil {
		t.Fatal("expected reject")
	}
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("got %#v want TOOL_ARGS_INVALID", err)
	}
	if rej.Data["reason"] != "invalid_scale" {
		t.Fatalf("reason: got %v want invalid_scale", rej.Data["reason"])
	}
}

func TestRenderViewToolHandleLifecycle(t *testing.T) {
	browsertest.SkipIfNoBrowser(t)
	reg := tools.NewDefaultRegistry()
	store := renderhandle.NewStore()
	if err := RegisterRenderViewTool(reg, nil, testRasterizer(t), store); err != nil {
		testutil.FailErr(t, "RegisterRenderViewTool failed", err)
	}

	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-render"},
		Effects:  tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	}

	t.Run("initial creation with handle", func(t *testing.T) {
		res, err := reg.Run(context.Background(), page.RenderViewToolName, map[string]any{
			"handle":  "login-card",
			"markup":  `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="50"><rect width="100" height="50" fill="#3b82f6"/></svg>`,
			"mime":    "svg",
			"caption": "Login Card Step 1",
		}, tctx)
		testutil.FailErr(t, "initial render_view with handle", err)

		if !strings.Contains(res, `"handle":"login-card"`) || !strings.Contains(res, `"revision":1`) {
			t.Fatalf("expected handle login-card and revision 1, got %s", res)
		}
		if tctx.Effects.Out.Visual == nil || !tctx.Effects.Out.Visual.Perceive {
			t.Fatalf("expected perceived visual, got %+v", tctx.Effects.Out.Visual)
		}
	})

	t.Run("surgical patch updates markup and increments revision", func(t *testing.T) {
		tctx.Effects.Out = &tools.ToolInvocationOut{}
		res2, err := reg.Run(context.Background(), page.RenderViewToolName, map[string]any{
			"handle":     "login-card",
			"old_string": `fill="#3b82f6"`,
			"new_string": `fill="#ef4444"`,
		}, tctx)
		testutil.FailErr(t, "surgical patch render_view", err)

		if !strings.Contains(res2, `"revision":2`) {
			t.Fatalf("expected revision 2 after patch, got %s", res2)
		}

		h, ok := store.Get(tctx.Identity.SessionID, "login-card")
		if !ok || h.Revision != 2 || !strings.Contains(h.Markup, `fill="#ef4444"`) {
			t.Fatalf("expected handle stored with red fill, got %+v", h)
		}
	})

	t.Run("patch target not found rejects RENDER_PATCH_NOT_FOUND", func(t *testing.T) {
		tctx.Effects.Out = &tools.ToolInvocationOut{}
		_, err := reg.Run(context.Background(), page.RenderViewToolName, map[string]any{
			"handle":     "login-card",
			"old_string": `nonexistent_string`,
			"new_string": `replacement`,
		}, tctx)
		if err == nil {
			t.Fatal("expected error on nonexistent patch string")
		}
		var rej *toolrejection.ToolReject
		if !errors.As(err, &rej) || rej.Code != "RENDER_PATCH_NOT_FOUND" {
			t.Fatalf("expected RENDER_PATCH_NOT_FOUND, got %#v", err)
		}
	})

	t.Run("nonexistent handle rejects RENDER_HANDLE_NOT_FOUND", func(t *testing.T) {
		tctx.Effects.Out = &tools.ToolInvocationOut{}
		_, err := reg.Run(context.Background(), page.RenderViewToolName, map[string]any{
			"handle":     "ghost-card",
			"old_string": `foo`,
			"new_string": `bar`,
		}, tctx)
		if err == nil {
			t.Fatal("expected error on nonexistent handle")
		}
		var rej *toolrejection.ToolReject
		if !errors.As(err, &rej) || rej.Code != "RENDER_HANDLE_NOT_FOUND" {
			t.Fatalf("expected RENDER_HANDLE_NOT_FOUND, got %#v", err)
		}
	})

	t.Run("re-render existing handle with theme without markup", func(t *testing.T) {
		tctx.Effects.Out = &tools.ToolInvocationOut{}
		res3, err := reg.Run(context.Background(), page.RenderViewToolName, map[string]any{
			"handle": "login-card",
			"theme":  "dark",
		}, tctx)
		testutil.FailErr(t, "re-render with theme", err)
		if !strings.Contains(res3, `"theme":"dark"`) || !strings.Contains(res3, `"revision":3`) {
			t.Fatalf("expected theme dark at revision 3, got %s", res3)
		}
	})
}

// A patch whose render fails leaves the committed markup and revision alone.
func TestRenderViewToolFailedPatchKeepsRevision(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := renderhandle.NewStore()
	testutil.FailErr(t, "RegisterRenderViewTool", RegisterRenderViewTool(reg, nil, testRasterizer(t), store))
	const sessionID = "sess-render"
	original := `<svg xmlns="http://www.w3.org/2000/svg"><rect fill="#3b82f6"/></svg>`
	_, err := store.Put(sessionID, &renderhandle.RenderHandle{ID: "card", Markup: original, Mime: "svg", Bytes: []byte("png")}, 0)
	testutil.FailErr(t, "seed handle", err)

	_, err = reg.Run(context.Background(), page.RenderViewToolName, map[string]any{
		"handle":     "card",
		"old_string": `<rect fill="#3b82f6"/>`,
		"new_string": `<script>alert(1)</script>`,
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID},
		Effects:  tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	})
	var rej *toolrejection.ToolReject
	if !errors.As(err, &rej) || rej.Code != "RENDER_MARKUP_FORBIDDEN" {
		t.Fatalf("err = %#v, want RENDER_MARKUP_FORBIDDEN", err)
	}
	h, ok := store.Get(sessionID, "card")
	if !ok || h.Markup != original || h.Revision != 1 {
		t.Fatalf("failed render changed the handle: %+v", h)
	}
}
