package prompts

import (
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpresentation"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

// RequestableToolView describes a deferred tool for turn-load scoring and receipts.
type RequestableToolView struct {
	Name    string
	Summary string
	Role    string
}

// capabilityLabels lists requestable capabilities in render order. IDs are
// presentation roles, except that the command role splits by which launchers
// remain requestable.
var capabilityLabels = []struct{ id, label string }{
	{"investigation", "repository investigation"},
	{"mutation", "file and git changes"},
	{capabilityCommandAndTerminal, "commands and interactive terminal sessions"},
	{capabilityCommandOnly, "commands and background output"},
	{capabilityTerminalOnly, "interactive terminal sessions and screen snapshots"},
	{"process", "processes"},
	{"interface_test", "browser pages and screenshots"},
	{"inspection", "viewing images, video, and rendered views"},
	{"research", "web research"},
	{"endpoint", "HTTP requests"},
	{"security_scan", "security scans"},
	{"automated_test", "automated verification"},
	{"coordination", "workers, workflows, budgets, and managed secrets"},
}

const (
	capabilityCommandAndTerminal = "command"
	capabilityCommandOnly        = "command_only"
	capabilityTerminalOnly       = "terminal_only"
)

// RequestableCapabilities lists the capability labels present in rows in a fixed order.
func RequestableCapabilities(rows []RequestableToolView) []string {
	present := requestableCapabilityIDs(rows)
	out := make([]string, 0, len(present))
	for _, c := range capabilityLabels {
		if present[c.id] {
			out = append(out, c.label)
		}
	}
	return out
}

// requestableCapabilityIDs skips resource controls: they arrive with their live
// resource, so requesting one ahead of it gains nothing.
func requestableCapabilityIDs(rows []RequestableToolView) map[string]bool {
	present := make(map[string]bool, len(rows))
	commandRole, command, terminal := false, false, false
	for _, row := range rows {
		if toolcontract.ResourceControl(row.Name) {
			continue
		}
		if row.Role != "command" {
			present[row.Role] = true
			continue
		}
		commandRole = true
		switch row.Name {
		case "command":
			command = true
		case "terminal_open":
			terminal = true
		}
	}
	switch {
	case !commandRole:
	case command && !terminal:
		present[capabilityCommandOnly] = true
	case terminal && !command:
		present[capabilityTerminalOnly] = true
	default:
		present[capabilityCommandAndTerminal] = true
	}
	return present
}

// SurfaceTurn holds session-loaded tools and tool schemas for a turn.
type SurfaceTurn struct {
	// Plan bounds catalog availability when a runtime snapshot is present.
	Plan    toolsurface.Plan
	Loaded  map[string]bool
	Schemas *toolschema.Config
}

// RequestableTools builds roster rows in the order given, truncating each
// schema description to the turn catalog's option_words limit.
func RequestableTools(names []string, schemas *toolschema.Config) ([]RequestableToolView, error) {
	catalog, err := turnload.LoadCatalog()
	if err != nil {
		return nil, err
	}
	out := make([]RequestableToolView, 0, len(names))
	for _, name := range names {
		view := RequestableToolView{Name: name, Role: toolpresentation.Role(name)}
		if schemas != nil {
			if meta, ok := schemas.ToolMeta(name); ok {
				view.Summary = turnload.OptionText(meta.Description, catalog.Turn.Tools.OptionWords)
			}
		}
		out = append(out, view)
	}
	return out, nil
}

func requestableToolVars(rows []RequestableToolView) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{"name": row.Name, "summary": row.Summary})
	}
	return out
}

// requestableToolSet returns deferred tools when request_tools is offered.
func requestableToolSet(offered, requestable []string) map[string]bool {
	plan := toolsurface.Compile(offered, requestable)
	out := map[string]bool{}
	if plan.Immediate("request_tools") {
		for _, name := range plan.DeferredNames() {
			out[name] = true
		}
	}
	return out
}

func requestableToolNames(rows []RequestableToolView) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names
}
