package session

import "testing"

func TestBuildToolPolicyNilManager(t *testing.T) {
	var mgr *Manager
	if mgr.PromptToolPolicy() == nil {
		t.Fatal("expected non-nil policy wrapper")
	}
}
