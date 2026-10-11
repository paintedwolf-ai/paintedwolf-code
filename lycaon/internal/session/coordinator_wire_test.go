package session

import (
	"testing"

	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
)

func TestToolPolicyWithoutOptionalDependencies(t *testing.T) {
	mgr := NewHost(sessionstore.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	if mgr.Coordinator.Guards.Policy() == nil {
		t.Fatal("expected policy without optional dependencies")
	}
}
