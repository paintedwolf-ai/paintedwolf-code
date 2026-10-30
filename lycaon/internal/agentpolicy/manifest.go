// Package agentpolicy identifies repository guidance delivered to one model request.
package agentpolicy

// Scope names a resolved path in the invocation's physical workspace.
type Scope struct {
	RootID   string
	RootPath string
	Path     string
}

// Revision identifies the source bytes represented by one policy block.
type Revision struct {
	RootID   string
	RootPath string
	Path     string
	Digest   string
}

// Manifest records the policy revisions included in one completion request.
type Manifest struct {
	Files []Revision
}

func (m Manifest) Clone() Manifest {
	return Manifest{Files: append([]Revision(nil), m.Files...)}
}

// Covers requires every currently applicable revision to have been delivered.
func (m Manifest) Covers(current Manifest) bool {
	delivered := make(map[Revision]struct{}, len(m.Files))
	for _, revision := range m.Files {
		delivered[revision] = struct{}{}
	}
	for _, revision := range current.Files {
		if _, ok := delivered[revision]; !ok {
			return false
		}
	}
	return true
}
