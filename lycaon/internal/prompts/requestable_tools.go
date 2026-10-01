package prompts

import (
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/toolpresentation"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// RequestableToolView is a loadable tool that is not on this call. The prompt
// names only the capability its role stands for; the summary is the bounded
// description the decision engine scores, kept for receipts and discovery.
type RequestableToolView struct {
	Name    string
	Summary string
	Role    string
}

// capabilityLabels names the capabilities that can still load, in a fixed
// order, one per activity role among the roster.
var capabilityLabels = []struct{ role, label string }{
	{"investigation", "repository investigation"},
	{"mutation", "file and git changes"},
	{"command", "commands and terminals"},
	{"process", "processes"},
	{"interface_test", "browser pages"},
	{"inspection", "viewing images, video, and rendered views"},
	{"research", "web search and page fetch"},
	{"endpoint", "HTTP requests"},
	{"security_scan", "security scans"},
	{"automated_test", "automated verification"},
	{"coordination", "workers, workflows, budgets, and managed secrets"},
}

// RequestableCapabilities lists the capability labels of a roster.
func RequestableCapabilities(rows []RequestableToolView) []string {
	present := make(map[string]bool, len(rows))
	for _, row := range rows {
		present[row.Role] = true
	}
	out := make([]string, 0, len(capabilityLabels))
	for _, c := range capabilityLabels {
		if present[c.role] {
			out = append(out, c.label)
		}
	}
	return out
}

// SurfaceTurn is what a turn adds to a static surface: the tools the session
// ledger has loaded, and the schemas that describe the rest.
type SurfaceTurn struct {
	Loaded  map[string]bool
	Schemas *toolschema.Config
}

// RequestableTools builds the roster rows for names, in the order given. The
// summary is the tool's schema description cut to the turn catalog's
// option_words; a name without a schema lists bare.
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

// requestableToolVars is the template shape of a roster.
func requestableToolVars(rows []RequestableToolView) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{"name": row.Name, "summary": row.Summary})
	}
	return out
}
