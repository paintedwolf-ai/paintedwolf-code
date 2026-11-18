package project

import (
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func outsideRoot(projectID, id string, addedAt time.Time) Root {
	return Root{ID: id, ProjectID: projectID, Path: projectroot.OutsidePath, Label: projectroot.OutsideLabel, AddedAt: addedAt, Kind: RootKindOutside}
}

func (p *Project) OutsideRef() (projectroot.RootRef, bool) {
	if p == nil || p.Outside.ID == "" {
		return projectroot.RootRef{}, false
	}
	return projectroot.OutsideRef(p.Outside.ID), true
}

// HistoryRoots includes the outside namespace so retained addresses resolve.
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

// WithOutside exposes the outside namespace after request-specific admission.
func WithOutside(p *Project) *Project {
	if p == nil || p.Outside.ID == "" {
		return p
	}
	for _, root := range p.Roots {
		if root.ID == p.Outside.ID {
			return p
		}
	}
	out := *p
	out.Roots = append(append([]Root(nil), p.Roots...), p.Outside)
	return &out
}
