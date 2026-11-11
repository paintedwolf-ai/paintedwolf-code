//go:build unix

package confine

import (
	"context"
	"math"
	"testing"
)

func TestParseChildLimit(t *testing.T) {
	for report, want := range map[string]uint64{"256\n": 256, "unlimited\n": math.MaxUint64} {
		if got, err := parseChildLimit(report); err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", report, got, err, want)
		}
	}
	for _, report := range []string{"", "many\n", "-1\n", "256\n256\n"} {
		if got, err := parseChildLimit(report); err == nil {
			t.Errorf("%q accepted as %d", report, got)
		}
	}
}

func TestChildDescriptorLimitReportsAFailedMeasurement(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if limit, err := ChildResourceLimit(ctx, "nofile"); err == nil {
		t.Fatalf("a canceled measurement reported %+v", limit)
	}
}
