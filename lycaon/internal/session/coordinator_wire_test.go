package session

import (
	"testing"

	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
)

func TestToolPolicyWithoutOptionalDependencies(t *testing.T) {
	mgr := NewManager(sessionstore.NewMemory(), nil, nil, settings.DefaultSessionLimits())
	if mgr.Guards.Policy() == nil {
		t.Fatal("expected policy without optional dependencies")
	}
}
