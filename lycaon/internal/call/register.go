package call

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// HandoffToolDeps wires handoff_* tool executors.
type HandoffToolDeps struct {
	Calls    CallManager
	Sessions SessionLookup
}

// RegisterHandoffTools registers handoff_* sibling coordination tools.
func RegisterHandoffTools(reg *tools.DefaultRegistry, deps HandoffToolDeps) error {
	if reg == nil || deps.Calls == nil {
		return fmt.Errorf("registry and call manager required")
	}
	return registerHandoffReservationTools(reg, deps)
}

func handoffSessionID(args map[string]any, tctx tools.ToolContext) string {
	if s, _ := args["session_id"].(string); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	if s := strings.TrimSpace(tctx.HandoffSessionID); s != "" {
		return s
	}
	if s := strings.TrimSpace(tctx.ParentSessionID); s != "" {
		return s
	}
	return strings.TrimSpace(tctx.SessionID)
}

func handoffAgentID(args map[string]any, tctx tools.ToolContext) string {
	if s, _ := args["agent"].(string); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	if s := strings.TrimSpace(tctx.HandoffAgentID); s != "" {
		return s
	}
	return strings.TrimSpace(tctx.Agent)
}

func parsePathList(raw any) ([]string, error) {
	switch items := raw.(type) {
	case []string:
		var out []string
		for _, s := range items {
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("paths required")
		}
		return out, nil
	case []any:
		var out []string
		for _, item := range items {
			s, _ := item.(string)
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("paths required")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("paths required")
	}
}
