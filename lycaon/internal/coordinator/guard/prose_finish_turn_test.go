package guard

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestProseFinishTurn(t *testing.T) {
	child := &api.Session{ParentSessionID: "parent-1"}
	coord := &api.Session{ID: "coord-1"}

	if ProseFinishTurn(coord, 8, 10) {
		t.Fatal("penultimate coordinator iter should still have tools")
	}
	if !ProseFinishTurn(coord, 9, 10) {
		t.Fatal("last coordinator iter should be prose-only")
	}
	if ProseFinishTurn(child, 8, 10) {
		t.Fatal("penultimate worker iter should still have tools")
	}
	if !ProseFinishTurn(child, 9, 10) {
		t.Fatal("last worker iter should be prose-only")
	}
	if ProseFinishTurn(nil, 9, 10) {
		t.Fatal("nil session")
	}
}

func TestProseTurnForcedEarlyCloseoutKeepsRealIndex(t *testing.T) {
	coord := &api.Session{ID: "coord-1"}
	if ProseTurn(coord, 9, 10, false) != true {
		t.Fatal("reserved last iter should gate invocation")
	}
	if ProseTurn(coord, 6, 500, false) {
		t.Fatal("mid-loop iter must still allow invocation")
	}
	if !ProseTurn(coord, 6, 500, true) {
		t.Fatal("forced early closeout must gate invocation")
	}
}
