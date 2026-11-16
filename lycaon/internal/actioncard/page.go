package actioncard

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func init() {
	register(pageResult, "page_act", "page_snapshot", "page_close")
}

var pageStep = map[string]string{"page_act": "act", "page_snapshot": "snapshot", "page_close": "close"}

// pageResult describes one call on an open page. Calls on one page share a
// fold key, and Den shows them as one card.
func pageResult(result api.ToolResult) Parts {
	parts := Parts{Facts: []api.ActionFact{{Kind: api.ActionFactSteps, Count: 1, Unit: "actions", Sequence: []string{pageStep[result.Tool]}}}}
	if id, _ := result.ToolArgs["id"].(string); strings.TrimSpace(id) != "" {
		parts.FoldKey = "page:" + strings.TrimSpace(id)
	}
	parts.Row = []api.ActionFactKind{api.ActionFactResult, api.ActionFactSteps}
	return parts
}
