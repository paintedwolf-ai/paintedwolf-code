package page

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestPageToolsRefuseInvalidTargetsAndMissingSessions(t *testing.T) {
	pages := pagesession.NewRegistry(pagesession.Config{MaxPages: 1})
	t.Cleanup(func() { pages.Close(t.Context()) })
	ctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}}
	cases := []struct {
		name    string
		handler tools.ToolHandler
		args    map[string]any
		code    string
	}{
		{"open missing target", OpenHandler(nil, pages, nil, nil), map[string]any{}, "CAPTURE_TARGET_INVALID"},
		{"open conflicting target", OpenHandler(nil, pages, nil, nil), map[string]any{"url": "https://example.com", "project_dir": "."}, "CAPTURE_TARGET_INVALID"},
		{"open path without root", OpenHandler(nil, pages, nil, nil), map[string]any{"url": "https://example.com", "path": "index.html"}, "CAPTURE_TARGET_INVALID"},
		{"open process with static root", OpenHandler(nil, pages, nil, nil), map[string]any{"project_dir": ".", "process_handle": "process"}, "CAPTURE_TARGET_INVALID"},
		{"act missing id", ActHandler(pages, nil), map[string]any{}, "PAGE_ID_REQUIRED"},
		{"act missing actions", ActHandler(pages, nil), map[string]any{"id": "page"}, "CAPTURE_TARGET_INVALID"},
		{"act missing session", ActHandler(pages, nil), map[string]any{"id": "page", "actions": []any{map[string]any{"type": "click", "selector": "button"}}}, "PAGE_NOT_FOUND"},
		{"snapshot missing id", SnapshotHandler(pages, nil), map[string]any{}, "PAGE_ID_REQUIRED"},
		{"snapshot missing session", SnapshotHandler(pages, nil), map[string]any{"id": "page"}, "PAGE_NOT_FOUND"},
		{"close missing id", CloseHandler(pages, nil), map[string]any{}, "PAGE_ID_REQUIRED"},
		{"close missing session", CloseHandler(pages, nil), map[string]any{"id": "page"}, "PAGE_NOT_FOUND"},
		{"measure missing target", MeasureHandler(nil, pages, nil), map[string]any{}, "CAPTURE_TARGET_INVALID"},
		{"measure held navigation", MeasureHandler(nil, pages, nil), map[string]any{"id": "page", "url": "https://example.com"}, "CAPTURE_TARGET_INVALID"},
		{"measure held selectors", MeasureHandler(nil, pages, nil), map[string]any{"id": "page"}, "MEASURE_SELECTORS_REQUIRED"},
		{"measure missing session", MeasureHandler(nil, pages, nil), map[string]any{"id": "page", "selectors": []string{"body"}}, "PAGE_NOT_FOUND"},
		{"capture missing target", CaptureHandler(nil, nil, nil), map[string]any{}, "CAPTURE_TARGET_INVALID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.handler(t.Context(), tc.args, ctx)
			var reject *toolrejection.ToolReject
			if out != "" || !errors.As(err, &reject) || reject.Code != tc.code {
				t.Fatalf("out=%q err=%v want=%s", out, err, tc.code)
			}
			if len(pages.List("session")) != 0 {
				t.Fatal("refused operation created a page")
			}
		})
	}
}

func TestRecordedFailurePreservesProjectedEvidenceAndOriginalReject(t *testing.T) {
	original := &browserengine.RejectError{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "action_failed"}}
	effect := &tools.ToolInvocationOut{}
	ctx := tools.ToolContext{Effects: tools.InvocationEffects{Out: effect}}
	capture := browser.CaptureResult{Mime: browser.FilmstripMime, Bytes: []byte("recording"), Timeline: &timelinearchive.Report{}}
	err := recordedFailure(ctx, capture, original)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != original.Code || reject.Data["recorded"] != true || reject.Data["timeline"] != capture.Timeline {
		t.Fatalf("recorded rejection=%v", err)
	}
	if _, exists := original.Data["recorded"]; exists {
		t.Fatal("recording mutated original rejection")
	}
	if effect.Visual == nil || !effect.Visual.Projected || effect.Visual.Perceive || string(effect.Visual.Bytes) != "recording" {
		t.Fatalf("lost projected recording: %+v", effect.Visual)
	}
	capture.Bytes[0] = 'X'
	if string(effect.Visual.Bytes) != "recording" {
		t.Fatal("visual aliases mutable source bytes")
	}
}

func TestPageOperationsReportUnavailableBrowserBeforeCreatingSessionState(t *testing.T) {
	pages := pagesession.NewRegistry(pagesession.Config{MaxPages: 1})
	t.Cleanup(func() { pages.Close(t.Context()) })
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}}
	live := &silentPreview{}
	for _, item := range []struct {
		handle tools.ToolHandler
		args   map[string]any
	}{
		{OpenHandler(nil, pages, nil, live), map[string]any{"url": "https://example.com", "viewport": map[string]any{"width": 320, "height": 180}}},
		{CaptureHandler(nil, nil, live), map[string]any{"url": "https://example.com", "viewport": map[string]any{"width": 320, "height": 180}, "capture": "timeline", "record": map[string]any{"tail_ms": 1}}},
		{MeasureHandler(nil, pages, nil), map[string]any{"url": "https://example.com", "selectors": []string{"body"}, "viewport": map[string]any{"width": 320, "height": 180}}},
	} {
		out, err := item.handle(t.Context(), item.args, tc)
		var reject *toolrejection.ToolReject
		if out != "" || !errors.As(err, &reject) || reject.Code != "BROWSER_UNAVAILABLE" {
			t.Fatalf("browser absence out=%q err=%v", out, err)
		}
		if len(pages.List("session")) != 0 || live.called {
			t.Fatal("unavailable browser created page or preview")
		}
	}
}

type silentPreview struct{ called bool }

func (p *silentPreview) Attach(context.Context, preview.AttachOpts)            { p.called = true }
func (p *silentPreview) Claim(context.Context, string, string, string, string) { p.called = true }
func (p *silentPreview) Detach(context.Context, string, string)                { p.called = true }
func (p *silentPreview) PublishAction(context.Context, string, string, browser.CaptureAction, json.RawMessage) {
	p.called = true
}
func (p *silentPreview) PublishDriving(context.Context, string, string, bool) { p.called = true }

func TestPageOpenRefusesProjectRootEscapeBeforeBrowserAccess(t *testing.T) {
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}}}
	out, err := OpenHandler(nil, nil, nil, nil)(t.Context(), map[string]any{"project_dir": ".."}, tc)
	var reject *toolrejection.ToolReject
	if out != "" || !errors.As(err, &reject) || reject.Code != "CAPTURE_NAVIGATION_DENIED" || reject.Data["capture_project_escape"] != true {
		t.Fatalf("project escape out=%q err=%v", out, err)
	}
}
