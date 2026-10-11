package phases

import (
	"testing"
)

func TestHostAutoAdvancedMarkerHelpers(t *testing.T) {
	if _, ok := hostAutoAdvancedFromPhase(nil); ok {
		t.Fatal("nil vars should not have marker")
	}
	if _, ok := hostAutoAdvancedFromPhase(map[string]any{"host_auto_advanced_from": "  "}); ok {
		t.Fatal("blank marker should be ignored")
	}
	vars := stampHostAutoAdvancedFrom(nil, "stub")
	if phase, ok := hostAutoAdvancedFromPhase(vars); !ok || phase != "stub" {
		t.Fatalf("vars = %+v", vars)
	}
	if phase, ok := hostAutoAdvancedFromPhase(stampHostAutoAdvancedFrom(vars, "")); !ok || phase != "stub" {
		t.Fatal("empty from phase should preserve existing marker")
	}
	cleared := clearHostAutoAdvancedMarker(vars)
	if _, ok := hostAutoAdvancedFromPhase(cleared); ok {
		t.Fatal("expected marker cleared")
	}
}
