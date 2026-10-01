package contract

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/sandbox"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Bundled config spells the project overlay directory as config.OverlayDirToken,
// because the real name follows the build channel; config.Read expands it. A
// reader that bypasses config.Read hands out the placeholder, which surfaces far
// away as a rejected manifest or copy naming a missing directory.

func TestPolicyCopyHasNoUnexpandedOverlayToken(t *testing.T) {
	t.Parallel()
	entries, err := hintregistry.ListEffective()
	contractcheck.FailErr(t, "list effective policy", err)
	if len(entries) == 0 {
		t.Fatal("no policy entries loaded; this test would pass vacuously")
	}
	for _, e := range entries {
		if bytes.Contains(e.Body, []byte(config.OverlayDirToken)) {
			t.Errorf("%s: overlay-dir placeholder reached agent-facing copy unexpanded", e.Path)
		}
	}
}

func TestPathScopesHaveNoUnexpandedOverlayToken(t *testing.T) {
	t.Parallel()
	scopes, err := sandbox.LoadPathScopes()
	contractcheck.FailErr(t, "load path scopes", err)
	if len(scopes) == 0 {
		t.Fatal("no path scopes loaded; this test would pass vacuously")
	}
	for id, scope := range scopes {
		for _, group := range [][]string{scope.Read, scope.Write, scope.Deny} {
			for _, glob := range group {
				if strings.Contains(glob, config.OverlayDirToken) {
					t.Errorf("path scope %q glob %q still holds the placeholder", id, glob)
				}
			}
		}
	}
}

// Every bundled workflow manifest must parse. The boot path loads them all, so a
// manifest the parser rejects takes the whole sidecar down.
func TestBundledWorkflowManifestsLoad(t *testing.T) {
	t.Parallel()
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "load bundled workflow manifests", err)
	if len(reg.List()) == 0 {
		t.Fatal("no bundled workflow manifests loaded")
	}
}
