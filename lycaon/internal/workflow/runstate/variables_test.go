package runstate

import "testing"

func TestForgetReleasesTerminalRunGuard(t *testing.T) {
	vars := NewVariables(nil, nil, nil)
	vars.Lock("terminal")()
	vars.Lock("active")()
	vars.Forget("terminal")
	if _, exists := vars.guards.Load("terminal"); exists {
		t.Fatal("terminal run guard retained")
	}
	if _, exists := vars.guards.Load("active"); !exists {
		t.Fatal("active run guard removed")
	}
}
