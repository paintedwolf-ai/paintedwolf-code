//go:build darwin && cgo

package hostpower

import (
	"testing"
)

func TestPlatformInhibitorAcquireRelease(t *testing.T) {
	inh := platformInhibitor{}
	if err := inh.Availability(); err != nil {
		t.Fatalf("platform inhibitor unavailable: %v", err)
	}

	lease, err := inh.Acquire()
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if lease == nil {
		t.Fatal("expected non-nil lease")
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	select {
	case <-lease.Done():
	default:
		t.Fatal("expected lease.Done() to be closed after release")
	}
}
