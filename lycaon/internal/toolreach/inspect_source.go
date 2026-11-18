package toolreach

// InspectSourceArgv builds one native read operation from typed arguments.
func InspectSourceArgv(args map[string]any) []string {
	path := stringArg(args, "path")
	operation := stringArg(args, "operation")
	switch operation {
	case "read":
		if path != "" {
			return []string{"cat", "-n", "--", path}
		}
	case "list":
		return appendValue([]string{"ls"}, "--", path)
	case "search":
		pattern := stringArg(args, "pattern")
		if pattern != "" {
			if path == "" {
				path = "."
			}
			return []string{"rg", "-n", "-e", pattern, "--", path}
		}
	case "query_json", "query_yaml":
		expression := stringArg(args, "query")
		if expression != "" && path != "" {
			words := []string{"jq"}
			if operation == "query_yaml" {
				words = append(words, "--yaml-input")
			}
			return append(words, "--", expression, path)
		}
	case "git_status":
		return appendValue([]string{"git", "status", "--short"}, "--", path)
	case "git_diff":
		return appendValue([]string{"git", "diff"}, "--", path)
	case "git_log":
		return appendValue([]string{"git", "log", "-n", "20", "--oneline"}, "--", path)
	}
	return nil
}
