package prompts

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
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

var capabilityLabels = []struct{ role, label string }{
	{"investigation", "repository investigation"},
	{"mutation", "file and git changes"},
	{"command", "commands and interactive terminal sessions"},
	{"process", "processes"},
	{"interface_test", "browser pages and screenshots"},
	{"inspection", "viewing images, video, and rendered views"},
	{"research", "web search and page fetch"},
	{"endpoint", "HTTP requests"},
	{"security_scan", "security scans"},
	{"automated_test", "automated verification"},
	{"coordination", "workers, workflows, budgets, and managed secrets"},
}

// RequestableCapabilities lists the capability labels present in rows in a fixed order.
func RequestableCapabilities(rows []RequestableToolView) []string {
	present := make(map[string]bool, len(rows))
	hasCommand := false
	hasTerminalSession := false
	for _, row := range rows {
		present[row.Role] = true
		if row.Name == "command" {
			hasCommand = true
		}
		if strings.HasPrefix(row.Name, "terminal_") {
			hasTerminalSession = true
		}
	}
	out := make([]string, 0, len(capabilityLabels))
	for _, c := range capabilityLabels {
		if !present[c.role] {
			continue
		}
		label := c.label
		if c.role == "command" {
			switch {
			case hasTerminalSession && !hasCommand:
				label = "interactive terminal sessions and screen snapshots"
			case !hasTerminalSession:
				label = "commands and background output"
			}
		}
		out = append(out, label)
	}
	return out
}

// SurfaceTurn holds session-loaded tools and tool schemas for a turn.
type SurfaceTurn struct {
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
