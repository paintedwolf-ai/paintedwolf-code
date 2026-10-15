package toolpresentation

import (
	"strconv"
	"strings"
)

// Title prepares the bounded subtitle while the original arguments are resident.
func Title(tool string, args map[string]any) string {
	tool = strings.ToLower(tool)
	if need, ok := args["need"].(string); ok && (tool == "request_tools" || tool == "skills_read") && strings.HasPrefix(strings.TrimSpace(need), "discovery:") {
		return "Available catalog"
	}
	if taskTools[tool] {
		return taskTitle(args)
	}
	if tool == "page_act" {
		return actionTitle(args)
	}
	if tool == "recall" {
		scope := map[string]string{"project": "all chats in this project", "all": "all chats, all projects"}[argLine(args["widen"])]
		return shortLine(joinTitle(scope, argLine(args["query"])))
	}
	if tool == "wait" {
		return waitTitle(args)
	}
	if text, handled := compositeTitle(tool, args); handled {
		return shortLine(text)
	}
	keys, declared := titleKeys[tool]
	if !declared {
		keys = fallbackTitleKeys
	}
	for _, key := range keys {
		if text := argLine(args[key]); text != "" {
			return shortLine(text)
		}
	}
	if declared {
		return titleFallbacks[tool]
	}
	return argLine(args["description"])
}

var waitConditionPriority = []string{
	"process_done",
	"http_ready",
	"port_ready",
	"overlay_promote_pending",
	"scan_done",
	"all_workers_idle",
	"next_worker_done",
}

func waitReason(args map[string]any) string {
	reason := argLine(args["reason"])
	if strings.EqualFold(reason, "waiting") {
		return ""
	}
	return reason
}

func waitTitle(args map[string]any) string {
	if reason := waitReason(args); reason != "" {
		return shortLine(reason)
	}
	conditions, _ := args["conditions"].([]any)
	for _, kind := range waitConditionPriority {
		for _, raw := range conditions {
			entry, ok := raw.(map[string]any)
			if ok && argLine(entry["kind"]) == kind {
				return waitConditionTitle(kind, entry)
			}
		}
	}
	return shortLine(argLine(args["reason"]))
}

func waitConditionTitle(kind string, entry map[string]any) string {
	switch kind {
	case "process_done":
		return "Waiting for a command to finish"
	case "http_ready":
		if url := argLine(entry["url"]); url != "" {
			return shortLine(joinTitle("Waiting for a service", url))
		}
		return "Waiting for a service"
	case "port_ready":
		if port := waitPort(entry["port"]); port != "" {
			host := argLine(entry["host"])
			if host == "" {
				host = "localhost"
			}
			return shortLine(joinTitle("Waiting for a port", host+":"+port))
		}
		return "Waiting for a port"
	case "overlay_promote_pending":
		return "Waiting to combine results"
	case "scan_done":
		return "Waiting for search to finish"
	default:
		return "Waiting for workers"
	}
}

func waitPort(value any) string {
	switch port := value.(type) {
	case float64:
		if port > 0 {
			return strconv.Itoa(int(port))
		}
	case int:
		if port > 0 {
			return strconv.Itoa(port)
		}
	case string:
		return argLine(port)
	}
	return ""
}

func shortLine(text string) string {
	return boundedPart(strings.TrimSpace(text), 96)
}

func argLine(value any) string {
	switch value := value.(type) {
	case string:
		for value != "" {
			line, rest, _ := strings.Cut(value, "\n")
			if line = strings.TrimSpace(line); line != "" {
				return shortLine(line)
			}
			value = rest
		}
	case []string:
		items := make([]any, len(value))
		for i, text := range value {
			items[i] = text
		}
		return argLine(items)
	case []any:
		var labels []string
		for _, item := range value {
			if text, ok := item.(string); ok {
				if line := argLine(text); line != "" {
					labels = append(labels, line)
				}
			}
			if object, ok := item.(map[string]any); ok {
				from, to := argLine(object["from"]), argLine(object["to"])
				if from != "" && to != "" {
					labels = append(labels, from+" → "+to)
				} else if from != "" {
					labels = append(labels, from)
				}
			}
		}
		return listTitle(labels)
	}
	return ""
}

func joinTitle(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + " · " + b
}

func taskTitle(args map[string]any) string {
	agent := argLine(args["subagent_type"])
	if agent == "" {
		agent = argLine(args["agent_type"])
	}
	goal := ""
	if brief, ok := args["brief"].(map[string]any); ok {
		goal = argLine(brief["goal"])
	}
	for _, key := range []string{"description", "task", "prompt"} {
		if goal == "" {
			goal = argLine(args[key])
		}
	}
	return shortLine(joinTitle(agent, goal))
}
