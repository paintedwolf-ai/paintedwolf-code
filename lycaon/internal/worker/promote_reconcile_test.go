package worker

import (
	"strings"
	"testing"
)

func TestProposeReconciledContentCleanThreeWay(t *testing.T) {
	c := PromoteConflict{
		Base:    "a\nb\n",
		Primary: "a\nb\n",
		Branch:  "a\nc\n",
	}
	proposed, reconciled := ProposeReconciledContent(c)
	if !reconciled {
		t.Fatal("expected reconciled")
	}
	if proposed != "a\nc\n" {
		t.Fatalf("proposed=%q", proposed)
	}
}

func TestProposeReconciledContentCombinesNonoverlappingEdits(t *testing.T) {
	c := PromoteConflict{
		Base:    "a\nb\nc\nd\ne\n",
		Primary: "a\nx\nc\nd\ne\n",
		Branch:  "a\nb\nc\ny\ne\n",
	}
	proposed, reconciled := ProposeReconciledContent(c)
	if !reconciled {
		t.Fatal("expected reconciled")
	}
	if !strings.Contains(proposed, "x") {
		t.Fatalf("primary sibling change missing: %q", proposed)
	}
	if !strings.Contains(proposed, "y") {
		t.Fatalf("branch intent missing: %q", proposed)
	}
}

func TestProposeReconciledContentRequiresChoiceForOverlappingEdits(t *testing.T) {
	c := PromoteConflict{Base: "value=base\n", Primary: "value=primary\n", Branch: "value=worker\n"}
	proposed, reconciled := ProposeReconciledContent(c)
	if reconciled || proposed != "" {
		t.Fatalf("overlap silently reconciled: %q (%v)", proposed, reconciled)
	}
}
