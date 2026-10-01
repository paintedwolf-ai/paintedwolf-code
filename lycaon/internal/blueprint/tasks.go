package blueprint

import (
	"encoding/json"
	"strings"
)

// Task is a decomposable blueprint work unit with verification commands and touched files.
type Task struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Files     []string `json:"files"`
	DependsOn []string `json:"depends_on,omitempty"`
	Verify    []string `json:"verify"`
}

const tasksMarkerStart = "<!-- lycaon:tasks"
const tasksMarkerEnd = "-->"

// ExtractTasks parses embedded JSON tasks from blueprint markdown content.
func ExtractTasks(content string) []Task {
	start := strings.Index(content, tasksMarkerStart)
	if start < 0 {
		return nil
	}
	rest := content[start+len(tasksMarkerStart):]
	end := strings.Index(rest, tasksMarkerEnd)
	if end < 0 {
		return nil
	}
	raw := strings.TrimSpace(rest[:end])
	var tasks []Task
	if err := json.Unmarshal([]byte(raw), &tasks); err != nil {
		return nil
	}
	return tasks
}

// CompletionCriteria builds leg criteria from a blueprint task.
func CompletionCriteria(task Task) []string {
	out := []string{"job:complete", "summary:present"}
	for _, f := range task.Files {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, "file:modified:"+f)
		}
	}
	for _, cmd := range task.Verify {
		cmd = strings.TrimSpace(cmd)
		if cmd != "" {
			out = append(out, "test:pass:"+cmd)
		}
	}
	return out
}
