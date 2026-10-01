package tooloutput_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tooloutput"
)

func TestOverlayToolResultMaxBytesFloor(t *testing.T) {
	if got := tooloutput.OverlayToolResultMaxBytes(0); got < 512<<10 {
		t.Fatalf("floor = %d", got)
	}
	if got := tooloutput.OverlayToolResultMaxBytes(131072); got < 512<<10 {
		t.Fatalf("boosted = %d", got)
	}
}
