package progress

import (
	"slices"
	"testing"
)

func TestUngatedLifecycleToolsKeepOfferedUngatedTools(t *testing.T) {
	offered := map[string]bool{"promote_overlay": true, "wait": true, "pack_board": true, "task": true}
	gated := func(tool string) bool { return tool == "wait" }
	got := ungatedLifecycleTools(offered, gated)
	if want := []string{"promote_overlay", "pack_board"}; !slices.Equal(got, want) {
		t.Fatalf("ungated lifecycle tools = %v, want %v", got, want)
	}
	if got := ungatedLifecycleTools(nil, gated); got != nil {
		t.Fatalf("nothing offered = %v, want none", got)
	}
}
