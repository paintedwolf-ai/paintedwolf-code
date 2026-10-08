package confine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
)

// addControlPlaneReadFloor keeps directory read grants from exposing control
// data nested within the approved path. The invocation's own session scratch
// stays readable; other sessions' scratch remains behind the floor.
func addControlPlaneReadFloor(rules *FilesystemRules, readRoots []string, sessionScratch string) {
	if secretReadDenyDisabled() {
		return
	}
	denied := resolveEach(controlPlaneReadDenyRoots())
	if len(denied) == 0 {
		return
	}
	rules.add(fsRule{op: opRead, matches: subpathMatches(denied, FloorReadControlPlane)})
	allowed := append(SecretReadAllowBackRoots(), resolveEach(readRoots)...)
	if sessionScratch != "" {
		allowed = append(allowed, fspath.CanonicalPath(sessionScratch))
	}
	if len(allowed) == 0 {
		return
	}
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches(allowed, "")})
	rules.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches(traversalAncestors(denied, allowed), "")})
}

// secretReadDenyDisabled reports an explicit read-floor opt-out.
func secretReadDenyDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LYCAON_SANDBOX_DENY_READ"))) {
	case "off", "none", "disable", "disabled":
		return true
	}
	return false
}

// requireReadFloorResolvable checks inputs before emitting the read floor.
func requireReadFloorResolvable() error {
	if !secretReadDenyDisabled() {
		if _, err := configdir.UserConfigDir(); err != nil {
			return fmt.Errorf("confine: control-plane read floor unresolvable: %w", err)
		}
	}
	// Key-material inputs remain required after a control-plane opt-out.
	if !homeRelPathsResolvable(catalogedKeyMaterialPaths()) {
		return fmt.Errorf("confine: key-material read floor unresolvable: home directory unreadable")
	}
	return nil
}

// homeRelPathsResolvable reports whether every ~/-relative entry can be
// joined onto a home directory.
func homeRelPathsResolvable(paths []string) bool {
	needsHome := false
	for _, p := range paths {
		if strings.HasPrefix(strings.TrimSpace(p), "~/") {
			needsHome = true
			break
		}
	}
	if !needsHome {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && strings.TrimSpace(home) != ""
}

func SecretReadDenyRoots() []string {
	if secretReadDenyDisabled() {
		return nil
	}
	env := strings.TrimSpace(os.Getenv("LYCAON_SANDBOX_DENY_READ"))
	raw := controlPlaneReadDenyRoots()
	for _, e := range filepath.SplitList(env) {
		if strings.TrimSpace(e) != "" {
			raw = append(raw, e)
		}
	}
	return resolveEach(raw)
}

// traversalAncestors returns denied parents needed for path traversal.
func traversalAncestors(deny, allow []string) []string {
	sep := string(filepath.Separator)
	inDeny := func(p string) bool {
		for _, d := range deny {
			d = filepath.Clean(d)
			if p == d || strings.HasPrefix(p, d+sep) {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range allow {
		for dir := filepath.Dir(filepath.Clean(a)); inDeny(dir); dir = filepath.Dir(dir) {
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// SecretReadAllowBackRoots returns readable workspace carve-outs.
func SecretReadAllowBackRoots() []string {
	if SecretReadDenyRoots() == nil {
		return nil
	}
	return resolveEach(agentWorkspaceRootsUnderControlPlane())
}

// agentWorkspaceRootsUnderControlPlane returns the engine-managed workspaces
// inside the state tree, independent of the read-floor switch.
func agentWorkspaceRootsUnderControlPlane() []string {
	dir, err := configdir.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return nil
	}
	return enginepaths.AgentWorkspaceRootsUnder(dir)
}

// controlPlaneReadDenyRoots returns the host-managed configuration tree.
func controlPlaneReadDenyRoots() []string {
	dir, err := configdir.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return nil
	}
	return []string{dir}
}

// resolveEach canonicalizes missing paths through their existing ancestor.
func resolveEach(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if rp := fspath.CanonicalPath(p); rp != "" {
			out = append(out, rp)
		} else {
			out = append(out, p)
		}
	}
	return out
}
