package confine_test

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Every request validates write grants at the confinement boundary.
func TestDefaultConfinementValidatesGrantedWriteRoots(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	if !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}

	// A sane request with only an attached root confines normally.
	if _, on := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}}); !on {
		t.Fatal("a plain attached root must confine")
	}

	// Home grants retain hard-deny protections for stores and the control plane.
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)
	if _, on := confine.DefaultConfinement(confine.Request{
		Roots:             []string{"/proj"},
		GrantedWriteRoots: []string{home},
	}); !on {
		t.Fatal("a granted write root equal to $HOME must confine, not refuse")
	}

	if _, on := confine.DefaultConfinement(confine.Request{
		Roots:             []string{"/proj"},
		GrantedWriteRoots: []string{"relative/not/absolute"},
	}); on {
		t.Fatal("a non-absolute granted write root must refuse confinement")
	}
}
