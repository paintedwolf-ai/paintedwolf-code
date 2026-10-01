package grammars

import (
	"strings"
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestPHPGiantArrayCommaListDefusalStaysLinear pins the quadratic-defusal side
// of the php comma-list gate (conflict_policy.go). The giant-data-array class
// this collapse exists for — Symfony emoji/Intl tables — is clean input that
// never enters C error handling, so the sticky glrStack.cEverErrored bit added
// for the wreckage-exclusion hardening stays false and the deterministic
// comma-list-repeat fold must still fire. When it fires the parse is single
// stack and linear (~9 nodes per element, MaxStacksSeen==1); if the gate ever
// stops firing on clean input (e.g. the sticky bit over-propagating to a
// never-errored lineage) the fold is lost and the flat comma spine detonates
// O(n^2) in allocated nodes and live stacks — the exact defusal this gate
// exists to prevent, and the emoji-witness regression review A flagged.
//
// Deterministic (node/stack counts, not wall clock) so it is CI-safe.
func TestPHPGiantArrayCommaListDefusalStaysLinear(t *testing.T) {
	const n = 6000
	var b strings.Builder
	b.Grow(n*3 + 32)
	b.WriteString("<?php $x = [")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("1")
	}
	b.WriteString("];\n")
	src := []byte(b.String())

	tree, err := ts.NewParser(PhpLanguage()).Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if tree.ParseStopReason() != ts.ParseStopAccepted {
		t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
	}
	if root.HasError() {
		t.Fatalf("clean giant array reported an error; the comma-list defusal must keep it clean")
	}
	if got := root.EndByte(); got != uint32(len(src)) {
		t.Fatalf("root end = %d, want %d (giant array not fully parsed)", got, len(src))
	}

	rt := tree.ParseRuntime()
	// Fold fired => linear (~9*n nodes). A disabled fold is O(n^2): the 20*n
	// bound clears the observed 9*n with slack while staying ~n below the
	// quadratic blow-up (n^2 = 36e6 at n=6000, vs the 120000 bound).
	if rt.NodesAllocated > 20*n {
		t.Fatalf("NodesAllocated=%d exceeds linear bound %d — comma-list quadratic defusal regressed (gate stopped folding clean input); runtime=%s",
			rt.NodesAllocated, 20*n, rt.Summary())
	}
	// The fold keeps the parse single-stack; a lost fold forks per element and
	// MaxStacksSeen climbs with input size.
	if rt.MaxStacksSeen > 16 {
		t.Fatalf("MaxStacksSeen=%d — clean giant array must stay effectively single-stack; the defusal regressed; runtime=%s",
			rt.MaxStacksSeen, rt.Summary())
	}
}
