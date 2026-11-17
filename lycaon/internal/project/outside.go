package project

import (
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func outsideRoot(projectID, id string, addedAt time.Time) Root {
	return Root{ID: id, ProjectID: projectID, Path: projectroot.OutsidePath, Label: projectroot.OutsideLabel, AddedAt: addedAt, Kind: RootKindOutside}
}

// OutsideRef is the project's outside namespace, absent for a project read
// without it.
func (p *Project) OutsideRef() (projectroot.RootRef, bool) {
	if p == nil || p.Outside.ID == "" {
		return projectroot.RootRef{}, false
	}
	return projectroot.OutsideRef(p.Outside.ID), true
}

// HistoryRoots are the namespaces source history addresses: every root, then
// the outside namespace.
func (p *Project) HistoryRoots() []Root {
	if p == nil {
		return nil
	}
	roots := append([]Root(nil), p.Roots...)
	if p.Outside.ID != "" {
		roots = append(roots, p.Outside)
	}
	return roots
}
