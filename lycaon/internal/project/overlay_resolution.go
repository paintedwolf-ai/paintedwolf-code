package project

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// OverlayResolution holds ordered project overlay roots.
type OverlayResolution struct {
	Primary projectroot.RootRef
	Active  projectroot.RootRef
	Roots   []projectroot.RootRef
	Paths   []string
}

// ResolveOverlay returns primary and active overlay roots.
func ResolveOverlay(roots []projectroot.RootRef, activeRootID string) (OverlayResolution, error) {
	if len(roots) == 0 {
		return OverlayResolution{}, nil
	}
	primary, err := projectroot.PrimaryRoot(roots)
	if err != nil {
		return OverlayResolution{}, fmt.Errorf("resolve project overlay primary root: %w", err)
	}
	active, err := projectroot.ActiveRoot(roots, activeRootID)
	if err != nil {
		active = primary
	}
	paths := []string{primary.Path}
	if active.Path != primary.Path {
		paths = append(paths, active.Path)
	}
	return OverlayResolution{
		Primary: primary,
		Active:  active,
		Roots:   append([]projectroot.RootRef(nil), roots...),
		Paths:   paths,
	}, nil
}

// ResolveProjectOverlay returns a registered project's overlay roots.
func ResolveProjectOverlay(p *Project, activeRootID string) (OverlayResolution, error) {
	return ResolveOverlay(RootRefsFrom(p), activeRootID)
}

// CheckCompatibility validates every resolved overlay format.
func (r OverlayResolution) CheckCompatibility() error {
	return settingsoverlay.CheckFormats(r.Paths)
}
