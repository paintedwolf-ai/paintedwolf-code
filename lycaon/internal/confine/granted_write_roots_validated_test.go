package confine_test

import (
	"os"
	"path/filepath"
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

	// A reviewed ancestor cannot move the host state tree.
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "fixture-state"))
	if _, on := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}, GrantedWriteRoots: []string{home}}); on {
		t.Fatal("state ancestor grant admitted")
	}

	if _, on := confine.DefaultConfinement(confine.Request{
		Roots:             []string{"/proj"},
		GrantedWriteRoots: []string{"relative/not/absolute"},
	}); on {
		t.Fatal("a non-absolute granted write root must refuse confinement")
	}
}
