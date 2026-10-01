// Package editorturn holds the host-defined execution boundaries that
// editor-action contributions select. Prompt copy, target validation, and
// admission live on the contribution invoke route; nothing here interprets a
// declaration.
package editorturn

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/contribution"
)

// InspectToolProfileID is the read-only surface for inspect presets.
const InspectToolProfileID = "explore_readonly"

// ToolProfileID is the file-writing surface for editor presets.
const ToolProfileID = "editor_file"

// PresetBoundary is the host-defined maximum boundary for one editor preset.
// Each value is fixed here; packs select a preset id and can never widen the
// boundary through prompts, configuration, MCP requirements, or tool names.
type PresetBoundary struct {
	// ToolProfile bounds the turn's tool surface.
	ToolProfile string
	// RequiresTarget: a structured file target is mandatory.
	RequiresTarget bool
	// RequiresFinding: a recorded host finding must name the target path.
	RequiresFinding bool
	// Writes: the turn may write at all; false leaves the read-only profile
	// with no write pin.
	Writes bool
	// SiblingWrites widens the write pin to the containing directory.
	SiblingWrites bool
}

// presetBoundaries is the closed preset implementation table.
var presetBoundaries = map[contribution.PresetID]PresetBoundary{
	contribution.PresetInspectFile: {
		ToolProfile:    InspectToolProfileID,
		RequiresTarget: true,
	},
	contribution.PresetEditFile: {
		ToolProfile:    ToolProfileID,
		RequiresTarget: true,
		Writes:         true,
	},
	contribution.PresetEditSibling: {
		ToolProfile:    ToolProfileID,
		RequiresTarget: true,
		Writes:         true,
		SiblingWrites:  true,
	},
	contribution.PresetFixFinding: {
		ToolProfile:     ToolProfileID,
		RequiresTarget:  true,
		RequiresFinding: true,
		Writes:          true,
	},
}

// PresetBoundaryFor returns the boundary for one closed preset id.
func PresetBoundaryFor(preset contribution.PresetID) (PresetBoundary, bool) {
	boundary, ok := presetBoundaries[preset]
	return boundary, ok
}

// ToolProfileIDs returns every tool profile a preset can select, sorted. An
// editor turn runs on the session the user addressed, never a spawned child.
func ToolProfileIDs() []string {
	seen := make(map[string]struct{}, len(presetBoundaries))
	out := make([]string, 0, len(presetBoundaries))
	for _, boundary := range presetBoundaries {
		id := strings.TrimSpace(boundary.ToolProfile)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// WritePins returns the maximal write pin for a target path under this
// boundary: the file itself, plus its containing directory for sibling
// presets. Nil means no writes at all.
func (b PresetBoundary) WritePins(path string) []string {
	path = strings.TrimSpace(path)
	if !b.Writes || path == "" {
		return nil
	}
	globs := []string{path}
	if b.SiblingWrites {
		dir := ""
		if i := strings.LastIndex(path, "/"); i >= 0 {
			dir = path[:i+1]
		}
		globs = append(globs, dir+"*")
	}
	return globs
}
