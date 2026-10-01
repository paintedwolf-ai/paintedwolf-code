package browser

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
)

func TestCDPUnavailableMapsPlainErrors(t *testing.T) {
	err := cdpUnavailable("page_failed", errors.New("connection reset by peer"))
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok {
		t.Fatalf("want browserengine.RejectError, got %T %v", err, err)
	}
	if rej.Code != "BROWSER_UNAVAILABLE" {
		t.Fatalf("code=%q", rej.Code)
	}
	if rej.Data["reason"] != "page_failed" {
		t.Fatalf("reason=%v", rej.Data["reason"])
	}
	if rej.Data["detail"] != "connection reset by peer" {
		t.Fatalf("detail=%v", rej.Data["detail"])
	}
}

func TestCDPUnavailablePassesRejectThrough(t *testing.T) {
	inner := browserengine.Reject("CAPTURE_URL_UNREACHABLE", map[string]any{"url": "x"})
	got := cdpUnavailable("page_failed", inner)
	if !errors.Is(got, inner) {
		t.Fatalf("expected same reject pointer")
	}
}
