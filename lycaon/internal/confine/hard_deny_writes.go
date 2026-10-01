package confine

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fspath"
)

// HardDenyWriteSpec describes one protected write target and its override layer.
type HardDenyWriteSpec struct {
	Layer FloorLayer
	// Literal is an exact absolute path.
	Literal string
	// Regex matches a structural path family; with Subpath it matches only
	// below that subpath.
	Regex string
	// Subpath protects existing and future descendants.
	Subpath string
	// ExceptSubpaths excludes managed workspaces from a control-tree deny.
	ExceptSubpaths []string
}

// HardDenyWriteSpecs returns resolved protected paths for a confinement.
func HardDenyWriteSpecs(c Confinement) ([]HardDenyWriteSpec, error) {
	out := make([]HardDenyWriteSpec, 0, 16)
	seenLit := map[string]bool{}
	addLit := func(layer FloorLayer, p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = fspath.CanonicalPath(p)
		key := string(layer) + "\x00" + p
		if seenLit[key] {
			return
		}
		seenLit[key] = true
		out = append(out, HardDenyWriteSpec{Layer: layer, Literal: p})
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve launcher for hard-deny: %w", err)
	}
	addLit(FloorControlPlane, exe)

	// Read roots remain read-only inside writable ancestors.
	for _, r := range c.ReadRoots {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, HardDenyWriteSpec{Layer: FloorBaseline, Subpath: fspath.CanonicalPath(r)})
		}
	}

	// Protected stores stay denied inside any attached root.
	for _, p := range append(keyMaterialTreeRoots(), CredentialStorePaths()...) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, HardDenyWriteSpec{Layer: FloorProtected, Subpath: fspath.CanonicalPath(p)})
		}
	}

	// Config discovery is required to protect the active control plane.
	userDir, err := configdir.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve control plane for hard-deny: %w", err)
	}
	if strings.TrimSpace(userDir) != "" {
		// Validated workspaces are re-allowed after this tree deny.
		out = append(out, HardDenyWriteSpec{Layer: FloorBaseline, Subpath: fspath.CanonicalPath(userDir)})
		exceptions := resolveEach(agentWorkspaceRootsUnderControlPlane())
		if c.SessionScratchRoot != "" {
			exceptions = append(exceptions, fspath.CanonicalPath(c.SessionScratchRoot))
		}
		out = append(out, HardDenyWriteSpec{
			Layer: FloorControlPlane, Subpath: fspath.CanonicalPath(userDir),
			ExceptSubpaths: exceptions,
		})
	}

	out = append(out, agentPolicySpecs(c.Roots)...)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Layer != out[j].Layer {
			return out[i].Layer < out[j].Layer
		}
		if out[i].Regex != out[j].Regex {
			return out[i].Regex < out[j].Regex
		}
		if out[i].Subpath != out[j].Subpath {
			return out[i].Subpath < out[j].Subpath
		}
		return out[i].Literal < out[j].Literal
	})
	return out, nil
}
