package layeredfs

import (
	"strings"
	"syscall"
)

// hiddenRel reports a path at or under engine storage its root contains.
func (c *Core) hiddenRel(qpath QualifiedPath) bool {
	for _, hidden := range c.roots.byID[qpath.RootID].Hidden {
		if qpath.Rel == hidden || strings.HasPrefix(qpath.Rel, hidden+"/") {
			return true
		}
	}
	return false
}

// refuseHiddenTarget stops a mutation from creating or replacing engine storage under its own name.
func (c *Core) refuseHiddenTarget(qpath QualifiedPath) error {
	if c.hiddenRel(qpath) {
		return syscall.EACCES
	}
	return nil
}

func cleanHiddenRels(rels []string) []string {
	var out []string
	for _, rel := range rels {
		if clean := cleanRelPath(rel); clean != "." && clean != "" {
			out = append(out, clean)
		}
	}
	return out
}
