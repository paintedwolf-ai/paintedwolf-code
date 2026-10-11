package projectsource

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
)

type Root struct {
	ID, ProjectID, Path, Label string
	IsPrimary                  bool
}

type Project struct {
	ID    string
	Roots []Root
}

func (p *Project) SourceID() string { return p.ID }

func (p *Project) SourceRoots() []projectroot.RootRef {
	if p == nil {
		return nil
	}
	roots := make([]projectroot.RootRef, len(p.Roots))
	for i, root := range p.Roots {
		roots[i] = projectroot.RootRef{ID: root.ID, Path: root.Path, Label: root.Label, IsPrimary: root.IsPrimary}
	}
	return roots
}

func (p *Project) WorkspaceID() string {
	if p == nil {
		return ""
	}
	return sourceworkspace.ID(p.ID, p.SourceRoots())
}

func fixtureProject(paths ...string) *Project {
	p := &Project{ID: "project"}
	for i, path := range paths {
		p.Roots = append(p.Roots, Root{ID: fmt.Sprintf("root-%d", i), Path: path, IsPrimary: i == 0})
	}
	return p
}
