package summarize

import "strings"

// DefaultTask supplies stable ranking text when task is omitted.
func DefaultTask(task, path string, paths []string, content string) string {
	if strings.TrimSpace(task) != "" {
		return strings.TrimSpace(task)
	}
	if path != "" {
		return "Overview of " + path
	}
	if len(paths) == 1 {
		return "Overview of " + paths[0]
	}
	if len(paths) > 1 {
		return "Overview of " + paths[0] + " and related paths"
	}
	if strings.TrimSpace(content) != "" {
		return "Overview of the attached material"
	}
	return "Overview"
}
