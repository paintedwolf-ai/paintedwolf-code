package toolsurface

import (
	"slices"
	"testing"
)

func TestPlanDistinguishesZeroClosedAndDeferred(t *testing.T) {
	var unrestricted Plan
	if unrestricted.Compiled() {
		t.Fatal("zero plan must mean no surface compile")
	}

	closed := Compile(nil, nil)
	if !closed.Compiled() || closed.Addressable("read") {
		t.Fatal("compiled empty plan must be closed")
	}

	plan := Compile([]string{"read"}, []string{"command"})
	if !plan.Immediate("read") || !plan.Deferred("command") || plan.Addressable("write") {
		t.Fatalf("unexpected availability: immediate=%v deferred=%v", plan.ImmediateNames(), plan.DeferredNames())
	}
}

func TestPlanPromotionAndRemovalDoNotMutateInput(t *testing.T) {
	base := Compile([]string{"read"}, []string{"command", "wait"})
	promoted := base.Promote("command").Without("wait")

	if !base.Deferred("command") || !base.Deferred("wait") {
		t.Fatalf("base mutated: immediate=%v deferred=%v", base.ImmediateNames(), base.DeferredNames())
	}
	if !slices.Equal(promoted.ImmediateNames(), []string{"command", "read"}) || len(promoted.DeferredNames()) != 0 {
		t.Fatalf("promoted plan = immediate %v deferred %v", promoted.ImmediateNames(), promoted.DeferredNames())
	}
}

func TestImmediatePlacementTakesPrecedence(t *testing.T) {
	plan := Compile([]string{"scan_list"}, []string{"scan_list"})
	if !plan.Immediate("scan_list") || plan.Deferred("scan_list") {
		t.Fatalf("scan_list mode = %v", plan.Availability("scan_list"))
	}
	plan = plan.Defer("scan_list")
	if !plan.Immediate("scan_list") {
		t.Fatal("defer must not demote an immediate tool")
	}
}
