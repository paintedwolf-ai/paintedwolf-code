package toolusage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// LoadGenerateTasks reads tasks from JSON lines.
func LoadGenerateTasks(path string) ([]GenerateTask, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-selected task file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var tasks []GenerateTask
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var task GenerateTask
		if err := json.Unmarshal([]byte(text), &task); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Prompt) == "" || seen[task.ID] {
			return nil, fmt.Errorf("%s:%d: a task needs a unique id and a prompt", path, line)
		}
		switch task.Posture {
		case "", wire.SessionPostureBuild, wire.SessionPostureVet, wire.SessionPostureSpec, wire.SessionPostureOrchestrate:
		default:
			return nil, fmt.Errorf("%s:%d: unknown posture %q", path, line, task.Posture)
		}
		if err := task.validateWorkflow(); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		seen[task.ID] = true
		tasks = append(tasks, task)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%s has no tasks", path)
	}
	return tasks, nil
}

func (task GenerateTask) validateWorkflow() error {
	if (strings.TrimSpace(task.Workflow) == "") != (strings.TrimSpace(task.WorkflowVersion) == "") {
		return fmt.Errorf("task %q requires workflow and workflow_version together", task.ID)
	}
	return nil
}
