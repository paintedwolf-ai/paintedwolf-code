package page

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestMissingPageRetainsInvocationAttributionWithoutPublishingEffects(t *testing.T) {
	pages := pagesession.NewRegistry(pagesession.DefaultConfig())
	t.Cleanup(func() { pages.Close(t.Context()) })
	for _, operation := range []string{"act", "snapshot", "close"} {
		t.Run(operation, func(t *testing.T) {
			live := &attributionPreview{}
			effects := &tools.ToolInvocationOut{}
			tc := tools.ToolContext{
				Identity:   tools.InvocationIdentity{SessionID: "caller", ToolCallID: "call"},
				Invocation: tools.Invocation{MessageID: "message"},
				Effects:    tools.InvocationEffects{Out: effects},
			}
			args := map[string]any{"id": "missing"}
			var handler tools.ToolHandler
			switch operation {
			case "act":
				handler = ActHandler(pages, live)
				args["actions"] = []any{map[string]any{"type": "click", "selector": "button"}}
			case "snapshot":
				handler = SnapshotHandler(pages, live)
			case "close":
				handler = CloseHandler(pages, live)
				args["snapshot"] = true
			}
			out, err := handler(t.Context(), args, tc)
			var reject *toolrejection.ToolReject
			if out != "" || !errors.As(err, &reject) || reject.Code != "PAGE_NOT_FOUND" || reject.Data["id"] != "missing" {
				t.Fatalf("missing page result=%q error=%v", out, err)
			}
			if live.claim != [4]string{"caller", "missing", "message", "call"} || live.claims != 1 {
				t.Fatalf("invocation attribution lost: %+v", live)
			}
			if live.effects != 0 || effects.Visual != nil || len(pages.List("caller")) != 0 {
				t.Fatal("missing page published browser effects or created session state")
			}
		})
	}
}

func TestRouteFixtureEscapeRefusesBeforePreviewClaim(t *testing.T) {
	live := &attributionPreview{}
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}}}
	out, err := ActHandler(nil, live)(t.Context(), map[string]any{
		"id": "page", "actions": []any{map[string]any{
			"type": "route", "routes": []any{map[string]any{"url": "https://example.com/data", "body_path": "../outside.json"}},
		}},
	}, tc)
	var reject *toolrejection.ToolReject
	if out != "" || !errors.As(err, &reject) || reject.Code != "CAPTURE_ROUTE_INVALID" || reject.Data["reason"] != "body_path_outside_workspace" || reject.Data["body_path"] != "../outside.json" {
		t.Fatalf("escaped route result=%q error=%v", out, err)
	}
	if live.claims != 0 || live.effects != 0 {
		t.Fatal("inadmissible fixture reached preview or browser execution")
	}
}

func TestSVGArtifactViewportRefusalKeepsStructuredBoundsAndNoVisual(t *testing.T) {
	store := visual.NewMemoryStore()
	source := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><rect width="1" height="1"/></svg>`)
	artifact, err := store.Put(t.Context(), "session", visual.Entry{
		Meta:  wire.VisualArtifact{Mime: "image/svg+xml", Source: wire.VisualArtifactSourceCapture, Perceive: true},
		Bytes: source,
	})
	testutil.FailErr(t, "store SVG artifact", err)
	effects := &tools.ToolInvocationOut{}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}, Effects: tools.InvocationEffects{Out: effects}}
	for _, item := range []struct {
		name   string
		raster *browser.Rasterizer
		args   map[string]any
		code   string
	}{
		{"unavailable renderer", nil, map[string]any{"handle": artifact.ID}, ""},
		{"invalid scale", &browser.Rasterizer{}, map[string]any{"handle": artifact.ID, "scale": 5}, "TOOL_ARGS_INVALID"},
		{"oversized viewport", &browser.Rasterizer{}, map[string]any{"handle": artifact.ID, "viewport": map[string]any{"width": browser.MaxViewportDim + 1, "height": 320}}, "RENDER_MARKUP_OVERSIZED"},
	} {
		t.Run(item.name, func(t *testing.T) {
			out, err := ViewImageHandler(ViewImageDeps{VisualStore: store, Raster: item.raster})(t.Context(), item.args, tc)
			if out != "" || err == nil || effects.Visual != nil {
				t.Fatalf("failed SVG rendering published result=%q error=%v visual=%+v", out, err, effects.Visual)
			}
			if item.code != "" {
				var reject *toolrejection.ToolReject
				if !errors.As(err, &reject) || reject.Code != item.code {
					t.Fatalf("SVG refusal lost structured code: %v", err)
				}
				if item.code == "RENDER_MARKUP_OVERSIZED" && (reject.Data["width"] != browser.MaxViewportDim+1 || reject.Data["height"] != 320 || reject.Data["render_viewport_exceeded"] != true) {
					t.Fatalf("SVG bounds lost: %v", reject.Data)
				}
			}
		})
	}
	// Rendering failure leaves the durable source available for a corrected viewport.
	_, ref := visual.ResolveRef(t.Context(), store, "session", artifact.ID)
	if !ref.IsPresent() || string(ref.Bytes()) != string(source) {
		t.Fatal("failed render changed the original artifact")
	}
}

type attributionPreview struct {
	claim           [4]string
	claims, effects int
}

func (p *attributionPreview) Claim(_ context.Context, session, page, message, call string) {
	p.claim, p.claims = [4]string{session, page, message, call}, p.claims+1
}
func (p *attributionPreview) Attach(context.Context, preview.AttachOpts) { p.effects++ }
func (p *attributionPreview) Detach(context.Context, string, string)     { p.effects++ }
func (p *attributionPreview) PublishAction(context.Context, string, string, browser.CaptureAction, json.RawMessage) {
	p.effects++
}
func (p *attributionPreview) PublishDriving(context.Context, string, string, bool) { p.effects++ }

func TestVideoArtifactCannotBeProjectedThroughImageTool(t *testing.T) {
	store := visual.NewMemoryStore()
	artifact, err := store.Put(t.Context(), "session", visual.Entry{
		Meta:  wire.VisualArtifact{Mime: "video/mp4", Source: wire.VisualArtifactSourceCapture, Perceive: true},
		Bytes: []byte("video fixture"),
	})
	testutil.FailErr(t, "store video artifact", err)
	effects := &tools.ToolInvocationOut{}
	out, err := ViewImageHandler(ViewImageDeps{VisualStore: store})(t.Context(), map[string]any{"handle": artifact.ID}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "session"}, Effects: tools.InvocationEffects{Out: effects},
	})
	var reject *toolrejection.ToolReject
	if out != "" || !errors.As(err, &reject) || reject.Code != "IMAGE_FORMAT_UNSUPPORTED" || reject.Data["handle"] != artifact.ID || reject.Data["extension"] != "video/mp4" || effects.Visual != nil {
		t.Fatalf("video artifact was not refused as an image: out=%q error=%v visual=%+v", out, err, effects.Visual)
	}
}

func TestCaptureProcessHandleRequiresURL(t *testing.T) {
	err := requireCaptureProcess(nil, "session", "", "process")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CAPTURE_TARGET_INVALID" || reject.Data["reason"] != "process_handle_without_url" {
		t.Fatalf("unbound process capture refusal=%v", err)
	}
}
