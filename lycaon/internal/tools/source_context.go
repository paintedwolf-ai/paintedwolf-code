package tools

import (
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
	root, path, ok := c.SourceLocation(value)
	if !ok {
		return
	}
	target := api.NavigationTarget{ProjectID: c.Identity.ProjectID, RootID: root.ID, Path: path, EntryKind: kind}
	if c.Source.WorkerBranchRoot != "" {
		target.WorkerID = c.Identity.WorkerJobID
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
