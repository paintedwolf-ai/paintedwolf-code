package confine

import "github.com/lycaon/lycaon/internal/fspath"

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
