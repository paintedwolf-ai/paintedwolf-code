package confine

// addControlPlaneReadFloor keeps directory read grants from exposing control
// data nested within the approved path.
func addControlPlaneReadFloor(rules *FilesystemRules, readRoots []string) {
	if secretReadDenyDisabled() {
		return
	}
	denied := resolveEach(controlPlaneReadDenyRoots())
	if len(denied) == 0 {
		return
	}
	rules.add(fsRule{op: opRead, matches: subpathMatches(denied, FloorReadControlPlane)})
	allowed := append(SecretReadAllowBackRoots(), resolveEach(readRoots)...)
	if len(allowed) == 0 {
		return
	}
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches(allowed, "")})
	rules.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches(traversalAncestors(denied, allowed), "")})
}
