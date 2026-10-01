package toolpresentation

import "strconv"

func actionTitle(args map[string]any) string {
	actions, _ := args["actions"].([]any)
	var labels []string
	for _, action := range actions {
		object, ok := action.(map[string]any)
		if ok {
			if label := actionStepTitle(object); label != "" {
				labels = append(labels, label)
			}
		}
	}
	title := listTitle(labels)
	if title != "" && args["record"] != nil && args["record"] != false {
		title = joinTitle(title, "recorded")
	}
	return shortLine(title)
}

func actionLocator(args map[string]any) string {
	for _, key := range []string{"label", "text", "testid", "selector", "role"} {
		if text := argLine(args[key]); text != "" {
			return text
		}
	}
	return ""
}

func actionStepTitle(args map[string]any) string {
	kind := argLine(args["type"])
	switch kind {
	case "", "wait", "wait_for":
		return ""
	case "press":
		return actionTarget("press", argLine(args["key"]))
	case "type":
		return actionTarget("type", argLine(args["value"]))
	case "drag":
		from := actionTarget("drag", actionLocator(args))
		to, _ := args["to"].(map[string]any)
		if target := actionLocator(to); target != "" {
			return from + " → " + target
		}
		return from
	case "scroll":
		target := actionLocator(args)
		if target == "" {
			target = "page"
		}
		return "scroll " + target
	case "route":
		routes, _ := args["routes"].([]any)
		if len(routes) == 0 {
			return "route"
		}
		first, _ := routes[0].(map[string]any)
		target := argLine(first["url"])
		if target == "" {
			return "route"
		}
		if len(routes) > 1 {
			target += " +" + strconv.Itoa(len(routes)-1)
		}
		return "route " + target
	default:
		target := actionLocator(args)
		if target == "" {
			target = argLine(args["value"])
		}
		return actionTarget(kind, target)
	}
}

func actionTarget(kind, target string) string {
	if target == "" {
		return kind
	}
	return kind + " " + target
}
