package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/page"
)

func captureFixtureRoot(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "test", "fixtures", "capture-page", name)
	abs, err := filepath.Abs(root)
	testutil.FailErr(t, "filepath.Abs failed", err)
	return abs
}

func TestMeasurePageToolRejectsMissingTarget(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	pages := pagesession.NewRegistry(pagesession.DefaultConfig())
	defer pages.Close(t.Context())
	if err := RegisterMeasurePageTool(reg, browser.NewPool(""), pages, nil); err != nil {
		testutil.FailErr(t, "RegisterMeasurePageTool failed", err)
	}
	_, err := reg.Run(context.Background(), page.MeasureToolName, map[string]any{
		"selectors": []any{"#box-a"},
	}, tools.ToolContext{Out: &tools.ToolInvocationOut{}})
	if err == nil {
		t.Fatal("expected reject")
	}
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "CAPTURE_TARGET_INVALID" {
		t.Fatalf("got %#v want CAPTURE_TARGET_INVALID", err)
	}
}

func TestMeasurePageToolRejectsEmptySelectors(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	pages := pagesession.NewRegistry(pagesession.DefaultConfig())
	defer pages.Close(t.Context())
	if err := RegisterMeasurePageTool(reg, browser.NewPool(""), pages, nil); err != nil {
		testutil.FailErr(t, "RegisterMeasurePageTool failed", err)
	}
	_, err := reg.Run(context.Background(), page.MeasureToolName, map[string]any{
		"project_dir": ".",
	}, tools.ToolContext{
		Out:   &tools.ToolInvocationOut{},
		Roots: []projectroot.RootRef{{ID: "main", Path: captureFixtureRoot(t, "geometry"), IsPrimary: true}},
	})
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "MEASURE_SELECTORS_REQUIRED" {
		t.Fatalf("got %#v want MEASURE_SELECTORS_REQUIRED", err)
	}
}

func TestMeasurePageToolRejectsLiveIDWithNavigationTarget(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	pages := pagesession.NewRegistry(pagesession.DefaultConfig())
	defer pages.Close(t.Context())
	testutil.FailErr(t, "RegisterMeasurePageTool", RegisterMeasurePageTool(reg, browser.NewPool(""), pages, nil))
	_, err := reg.Run(context.Background(), page.MeasureToolName, map[string]any{
		"id": "page-1", "url": "http://127.0.0.1:3000", "selectors": []any{"#target"},
	}, tools.ToolContext{SessionID: "s", Out: &tools.ToolInvocationOut{}})
	rej := &toolrejection.ToolReject{}
	if !errors.As(err, &rej) || rej.Code != "CAPTURE_TARGET_INVALID" {
		t.Fatalf("got %#v want CAPTURE_TARGET_INVALID", err)
	}
}
