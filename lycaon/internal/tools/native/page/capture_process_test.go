package page

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRequireCaptureProcess_optionalWhenEmpty(t *testing.T) {
	if err := requireCaptureProcess(nil, "sess", "http://127.0.0.1:1/", ""); err != nil {
		testutil.FailErr(t, "requireCaptureProcess failed", err)
	}
}

func TestRequireCaptureProcess_missingHandle(t *testing.T) {
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	err := requireCaptureProcess(reg, "sess", "http://127.0.0.1:1/", "missing-handle")
	rej := &tools.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "CAPTURE_PROCESS_NOT_RUNNING" {
		t.Fatalf("got %#v", err)
	}
}
