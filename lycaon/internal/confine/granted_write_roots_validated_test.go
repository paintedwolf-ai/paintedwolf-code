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
		t.Skip("this host has no confinement adapter")
	}

	// A sane request with only an attached root confines normally.
	if _, on := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}}); !on {
		t.Fatal("a plain attached root must confine")
	}

	// A reviewed ancestor confines while the control-plane and ancestor-spine floors protect host state;
	// a direct control-plane grant still refuses confinement.
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)
	state := filepath.Join(home, ".config", "fixture-state")
	t.Setenv("LYCAON_CONFIG_DIR", state)
	if _, on := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}, GrantedWriteRoots: []string{home}}); !on {
		t.Fatal("state ancestor grant must confine with floor protection")
	}
	if _, on := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}, GrantedWriteRoots: []string{state}}); on {
		t.Fatal("direct control-plane grant admitted")
	}

	if _, on := confine.DefaultConfinement(confine.Request{
		Roots:             []string{"/proj"},
		GrantedWriteRoots: []string{"relative/not/absolute"},
	}); on {
		t.Fatal("a non-absolute granted write root must refuse confinement")
	}
}
