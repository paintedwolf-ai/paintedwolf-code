package project

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/pkg/api"
)

// ResolveNavigation validates fixed destinations without catalog discovery.
func ResolveNavigation(ctx context.Context, p *Project, refs []api.NavigationReference) []api.NavigationReference {
	out := make([]api.NavigationReference, 0, len(refs))
	for _, ref := range refs {
		switch {
		case ctx.Err() != nil:
			ref.Status = api.NavigationPending
		case ref.Status == api.NavigationAmbiguous:
			// Candidates retain their producer workspace; selecting one validates it there.
		case ref.RootID == "":
			ref.Status = api.NavigationUnavailable
		default:
			ref = resolveExactNavigation(p, ref)
		}
		out = append(out, ref)
	}
	return out
}

func resolveExactNavigation(p *Project, ref api.NavigationReference) api.NavigationReference {
	ref.Candidates = nil
	if ref.RootID != "" {
		attached := false
		for _, root := range p.Roots {
			attached = attached || root.ID == ref.RootID
		}
		if !attached {
			ref.Status = api.NavigationUnavailable
			return ref
		}
	}
	target, err := navigationTarget(p, ref.RootID, ref.Path)
	if err != nil {
		ref.Status = api.NavigationUnavailable
		if errors.Is(err, projectsource.ErrSourceNotFound) {
			ref.Status = api.NavigationMissing
		}
		return ref
	}
	ref.RootID, ref.Path, ref.EntryKind = target.RootID, target.Path, target.EntryKind
	ref.Status = api.NavigationResolved
	return ref
}
func navigationTarget(p *Project, rootID, rel string) (api.NavigationTarget, error) {
	resolved, err := projectsource.ResolveProjectPath(p, rootID, rel)
	if err != nil {
		return api.NavigationTarget{}, err
	}
	target := api.NavigationTarget{ProjectID: p.ID, RootID: resolved.RootID, Path: resolved.Path, EntryKind: api.NavigationEntryKind(resolved.EntryKind)}
	return target, nil
}
