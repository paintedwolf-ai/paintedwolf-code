package tools

import (
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecordSourceLocation records a location explicitly represented by a tool result.
func (c ToolContext) RecordSourceLocation(target api.NavigationTarget) {
	if c.Out == nil || target.ProjectID != c.ProjectID {
		return
	}
	if !sourceref.Valid(target) {
		return
	}
	key := sourceref.Key(target)
	if c.Out.sourceLocations == nil {
		c.Out.sourceLocations = make(map[string]struct{})
	}
	if _, exists := c.Out.sourceLocations[key]; exists {
		return
	}
	if c.Out.SourceContext == nil {
		c.Out.SourceContext = &api.SourceContext{Locations: []api.NavigationTarget{}}
	}
	if len(c.Out.SourceContext.Locations) >= api.MaxSourceContextLocations {
		c.Out.SourceContext.Truncated = true
		return
	}
	c.Out.sourceLocations[key] = struct{}{}
	c.Out.SourceContext.Locations = append(c.Out.SourceContext.Locations, target)
}

// RecordSourcePath resolves a structured producer path in its workspace.
func (c ToolContext) RecordSourcePath(value string, kind api.NavigationEntryKind) {
	if c.Out == nil || c.ProjectID == "" {
		return
	}
	root, path, ok := c.SourceLocation(value)
	if !ok {
		return
	}
	target := api.NavigationTarget{ProjectID: c.ProjectID, RootID: root.ID, Path: path, EntryKind: kind}
	if c.WorkerBranchRoot != "" {
		target.WorkerID = c.WorkerJobID
	}
	c.RecordSourceLocation(target)
}

// RecordSourceContext merges a recalled source context into the tool result.
func (c ToolContext) RecordSourceContext(context *api.SourceContext) {
	if context == nil || c.Out == nil {
		return
	}
	for _, target := range context.Locations {
		c.RecordSourceLocation(target)
	}
	if context.Truncated {
		if c.Out.SourceContext == nil {
			c.Out.SourceContext = &api.SourceContext{Locations: []api.NavigationTarget{}}
		}
		c.Out.SourceContext.Truncated = true
	}
}
