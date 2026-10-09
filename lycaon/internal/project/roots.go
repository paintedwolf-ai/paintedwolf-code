package project

import (
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
)

// SourceID binds source effects to the durable project identity.
func (p *Project) SourceID() string {
	if p == nil {
		return ""
	}
	return p.ID
}

// SourceRoots exposes only attached filesystem facts to source operations.
func (p *Project) SourceRoots() []projectroot.RootRef {
	return RootRefsFrom(p)
}

// RootPaths returns unique project root paths, primary first.
func RootPaths(p *Project) []string {
	if p == nil || len(p.Roots) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.Roots))
	seen := make(map[string]struct{}, len(p.Roots))
	appendRoot := func(r Root) {
		path := strings.TrimSpace(r.Path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	for _, r := range p.Roots {
		if r.IsPrimary {
			appendRoot(r)
		}
	}
	for _, r := range p.Roots {
		if !r.IsPrimary {
			appendRoot(r)
		}
	}
	return out
}

// RootRefsFrom converts persisted project roots to path-resolution refs (primary first).
func RootRefsFrom(p *Project) []projectroot.RootRef {
	if p == nil || len(p.Roots) == 0 {
		return nil
	}
	out := make([]projectroot.RootRef, 0, len(p.Roots))
	for _, r := range p.Roots {
		if r.IsPrimary {
			out = append(out, rootRefFrom(r))
		}
	}
	for _, r := range p.Roots {
		if !r.IsPrimary {
			out = append(out, rootRefFrom(r))
		}
	}
	return out
}

func rootRefFrom(r Root) projectroot.RootRef {
	return projectroot.RootRef{
		ID:        r.ID,
		Label:     r.Label,
		Path:      r.Path,
		IsPrimary: r.IsPrimary,
	}
}
