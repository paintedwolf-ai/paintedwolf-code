package worker

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

func parseTaskCharter(args map[string]any) (api.WorkerTaskCharter, error) {
	raw, ok := args["brief"].(map[string]any)
	if !ok {
		return api.WorkerTaskCharter{}, invalidTaskCharter("brief", "missing_brief")
	}
	goal, err := requiredTaskCharterString(raw, "goal")
	if err != nil {
		return api.WorkerTaskCharter{}, err
	}
	doneWhen, err := taskCharterStrings(raw, "done_when", true)
	if err != nil {
		return api.WorkerTaskCharter{}, err
	}
	knownFacts, err := taskCharterStrings(raw, "known_facts", false)
	if err != nil {
		return api.WorkerTaskCharter{}, err
	}
	constraints, err := taskCharterStrings(raw, "constraints", false)
	if err != nil {
		return api.WorkerTaskCharter{}, err
	}
	contextRefs, err := taskCharterStrings(raw, "context_refs", false)
	if err != nil {
		return api.WorkerTaskCharter{}, err
	}
	charter := api.WorkerTaskCharter{
		Goal: goal, KnownFacts: knownFacts, Constraints: constraints,
		DoneWhen: doneWhen, ContextRefs: contextRefs,
	}
	if spawn.TaskCharterRunes(charter) > spawn.MaxTaskCharterRunes {
		return api.WorkerTaskCharter{}, &toolrejection.ToolReject{
			Code: "TOOL_ARGS_INVALID",
			Data: map[string]any{
				"field": "brief", "tool": "task", "reason": "brief_too_long",
				"max_runes": spawn.MaxTaskCharterRunes,
			},
		}
	}
	if value, exists := args["shared_context"]; exists {
		text, ok := value.(string)
		if !ok || len(text) > 8192 {
			return api.WorkerTaskCharter{}, invalidTaskCharter("shared_context", "expected_text_up_to_8192_bytes")
		}
		charter.SharedContext = strings.TrimSpace(guidance.StripHostBlocks(text))
	}
	return charter, nil
}

func requiredTaskCharterString(raw map[string]any, field string) (string, error) {
	rawValue, exists := raw[field]
	if !exists || rawValue == nil {
		return "", invalidTaskCharter("brief."+field, "required_string")
	}
	value, ok := rawValue.(string)
	if !ok {
		return "", invalidTaskCharter("brief."+field, "invalid_string")
	}
	value = strings.TrimSpace(guidance.StripHostBlocks(strings.TrimSpace(value)))
	if value == "" {
		return "", invalidTaskCharter("brief."+field, "required_string")
	}
	return value, nil
}

func taskCharterStrings(raw map[string]any, field string, required bool) ([]string, error) {
	value, exists := raw[field]
	if !exists || value == nil {
		if required {
			return nil, invalidTaskCharter("brief."+field, "required_string_array")
		}
		return nil, nil
	}
	var items []string
	switch values := value.(type) {
	case []any:
		items = make([]string, 0, len(values))
		for _, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, invalidTaskCharter("brief."+field, "invalid_string_array")
			}
			items = append(items, text)
		}
	case []string:
		items = append([]string(nil), values...)
	default:
		return nil, invalidTaskCharter("brief."+field, "invalid_string_array")
	}
	for i := range items {
		items[i] = strings.TrimSpace(guidance.StripHostBlocks(strings.TrimSpace(items[i])))
		if items[i] == "" {
			return nil, invalidTaskCharter("brief."+field, "empty_array_item")
		}
	}
	if required && len(items) == 0 {
		return nil, invalidTaskCharter("brief."+field, "required_string_array")
	}
	return items, nil
}

func formatTaskCharter(charter api.WorkerTaskCharter) string {
	var out strings.Builder
	out.WriteString("Goal:\n")
	out.WriteString(charter.Goal)
	for _, section := range []struct {
		title string
		items []string
	}{
		{title: "Known facts", items: charter.KnownFacts},
		{title: "Constraints", items: charter.Constraints},
		{title: "Done when", items: charter.DoneWhen},
		{title: "Context references", items: charter.ContextRefs},
	} {
		if len(section.items) == 0 {
			continue
		}
		out.WriteString("\n\n")
		out.WriteString(section.title)
		out.WriteString(":\n- ")
		out.WriteString(strings.Join(section.items, "\n- "))
	}
	if charter.SharedContext != "" {
		out.WriteString("\n\nShared context:\n" + charter.SharedContext)
	}
	return out.String()
}

func invalidTaskCharter(field, reason string) error {
	return &toolrejection.ToolReject{
		Code: "TOOL_ARGS_INVALID",
		Data: map[string]any{"field": field, "tool": "task", "reason": reason},
	}
}

func plannedTaskCharter(planned *api.WorkerTaskCharter, args map[string]any) (api.WorkerTaskCharter, error) {
	if planned == nil {
		return parseTaskCharter(args)
	}
	charter := *planned
	if strings.TrimSpace(charter.Goal) == "" || len(charter.DoneWhen) == 0 || spawn.TaskCharterRunes(charter) > spawn.MaxTaskCharterRunes {
		return charter, invalidTaskCharter("workflow_work_id", "invalid_planned_charter")
	}
	if _, given := args["brief"]; given {
		supplemental, err := parseTaskCharter(args)
		if err != nil {
			return charter, err
		}
		charter.SharedContext += "\n\nCoordinator supplement:\n" + formatTaskCharter(supplemental)
	} else if value, exists := args["shared_context"]; exists {
		text, ok := value.(string)
		if !ok || len(text) > 8192 {
			return charter, invalidTaskCharter("shared_context", "expected_text_up_to_8192_bytes")
		}
		charter.SharedContext += "\n\n" + guidance.StripHostBlocks(text)
	}
	if len(charter.SharedContext) > 16384 {
		return charter, invalidTaskCharter("shared_context", "combined_context_too_long")
	}
	return charter, nil
}
