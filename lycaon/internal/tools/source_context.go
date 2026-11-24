package tools

import (
	"context"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecordSourceLocation records a location explicitly represented by a tool result.
func (c ToolContext) RecordSourceLocation(target api.NavigationTarget) {
	if c.Effects.Out == nil || target.ProjectID != c.Identity.ProjectID || !sourceref.Valid(target) {
		return
	}
	c.Effects.Out.recordSourceLocation(target)
}

func (c ToolContext) enrichCheckoutTarget(target *api.NavigationTarget, absPath string) {
	if c.Source.CheckoutUsage == nil || c.Source.WorkerBranchRoot != "" || absPath == "" {
		return
	}
	resolved, ok := c.Source.CheckoutUsage.ResolvePathCheckout(context.Background(), c.Identity.ProjectID, absPath)
	if !ok {
		return
	}
	target.RepoID = resolved.RepoID
	target.WorktreeID = resolved.WorktreeID
	if resolved.RootID != "" && resolved.RootRel != "" && resolved.RootRel != "." {
		target.RootID = resolved.RootID
		target.Path = resolved.RootRel
		target.FileID = ""
	}
}

// RecordSourceIdentity records a location the tool already resolved, so the
// result carries {RootID, ScopeRel} instead of re-resolving a display string.
func (c ToolContext) RecordSourceIdentity(rootID, scopeRel string, kind api.NavigationEntryKind) {
	if c.Effects.Out == nil || c.Identity.ProjectID == "" {
		return
	}
	if rootID == "" || scopeRel == "" || scopeRel == "." {
		return
	}
	target := api.NavigationTarget{ProjectID: c.Identity.ProjectID, RootID: rootID, Path: scopeRel, EntryKind: kind}
	if c.Source.WorkerBranchRoot != "" {
		target.WorkerID = c.Identity.WorkerJobID
	} else {
		for _, r := range c.Source.Roots {
			if r.ID == rootID && r.Path != "" {
				c.enrichCheckoutTarget(&target, filepath.Join(r.Path, filepath.FromSlash(scopeRel)))
				break
			}
		}
	}
	c.RecordSourceLocation(target)
}

// RecordSourcePath resolves a structured producer path in its workspace.
func (c ToolContext) RecordSourcePath(value string, kind api.NavigationEntryKind) {
	if c.Effects.Out == nil || c.Identity.ProjectID == "" {
		return
	}
	if c.Effects.Out.SourceContext != nil && c.Effects.Out.SourceContext.Truncated {
		return
	}
	if c.Source.WorkerBranchRoot == "" && c.Source.CheckoutUsage != nil && filepath.IsAbs(value) {
		if resolved, ok := c.Source.CheckoutUsage.ResolvePathCheckout(context.Background(), c.Identity.ProjectID, value); ok && resolved.RootID != "" && resolved.RootRel != "" && resolved.RootRel != "." {
			c.RecordSourceLocation(api.NavigationTarget{
				ProjectID:  c.Identity.ProjectID,
				RepoID:     resolved.RepoID,
				WorktreeID: resolved.WorktreeID,
				RootID:     resolved.RootID,
				Path:       resolved.RootRel,
				EntryKind:  kind,
			})
			return
		}
	}
	root, path, ok := c.RecordedLocation(value)
	if !ok {
		return
	}
	target := api.NavigationTarget{ProjectID: c.Identity.ProjectID, RootID: root.ID, Path: path, EntryKind: kind}
	if c.Source.WorkerBranchRoot != "" {
		target.WorkerID = c.Identity.WorkerJobID
	} else if root.Path != "" {
		c.enrichCheckoutTarget(&target, filepath.Join(root.Path, filepath.FromSlash(path)))
	}
	c.RecordSourceLocation(target)
}

// RecordSourceContext merges a recalled source context into the tool result.
func (c ToolContext) RecordSourceContext(context *api.SourceContext) {
	if context == nil || c.Effects.Out == nil {
		return
	}
	for _, target := range context.Locations {
		c.RecordSourceLocation(target)
	}
	if context.Truncated {
		c.Effects.Out.truncateSourceContext()
	}
}
