package tools

import (
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecordSourceLocation records a location explicitly represented by a tool result.
func (c ToolContext) RecordSourceLocation(target api.NavigationTarget) {
	if c.Effects.Out == nil || target.ProjectID != c.Identity.ProjectID {
		return
	}
	if !sourceref.Valid(target) {
		return
	}
	key := sourceref.Key(target)
	if c.Effects.Out.sourceLocations == nil {
		c.Effects.Out.sourceLocations = make(map[string]struct{})
	}
	if _, exists := c.Effects.Out.sourceLocations[key]; exists {
		return
	}
	if c.Effects.Out.SourceContext == nil {
		c.Effects.Out.SourceContext = &api.SourceContext{Locations: []api.NavigationTarget{}}
	}
	if len(c.Effects.Out.SourceContext.Locations) >= api.MaxSourceContextLocations {
		c.Effects.Out.SourceContext.Truncated = true
		return
	}
	c.Effects.Out.sourceLocations[key] = struct{}{}
	c.Effects.Out.SourceContext.Locations = append(c.Effects.Out.SourceContext.Locations, target)
}

// RecordSourcePath resolves a structured producer path in its workspace.
func (c ToolContext) RecordSourcePath(value string, kind api.NavigationEntryKind) {
	if c.Effects.Out == nil || c.Identity.ProjectID == "" {
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
		if c.Effects.Out.SourceContext == nil {
			c.Effects.Out.SourceContext = &api.SourceContext{Locations: []api.NavigationTarget{}}
		}
		c.Effects.Out.SourceContext.Truncated = true
	}
}
