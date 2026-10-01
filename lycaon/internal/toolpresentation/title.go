package toolpresentation

import "strings"

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
