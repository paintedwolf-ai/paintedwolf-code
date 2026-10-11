package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/page"
)

func TestCapturePageToolRejectsMissingTarget(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	if err := RegisterCapturePageTool(reg, browser.NewPool(""), nil, nil); err != nil {
		testutil.FailErr(t, "RegisterCapturePageTool failed", err)
	}
	_, err := reg.Run(context.Background(), page.CaptureToolName, map[string]any{}, tools.ToolContext{
		Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	})
	if err == nil {
		t.Fatal("expected reject")
	}
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "CAPTURE_TARGET_INVALID" {
		t.Fatalf("got %#v want CAPTURE_TARGET_INVALID", err)
	}
}
