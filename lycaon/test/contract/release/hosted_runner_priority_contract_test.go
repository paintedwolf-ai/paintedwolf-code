package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

const runnerScheduler = "runner-priority.yml"

type prioritizedWorkflow struct {
	Name        string
	On          map[string]yaml.Node
	Permissions map[string]string
	Concurrency struct {
		Group  string
		Cancel string `yaml:"cancel-in-progress"`
	}
	Jobs map[string]yaml.Node
}

func prioritizedWorkflows(t *testing.T) map[string]prioritizedWorkflow {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	contractcheck.FailErr(t, "read workflows", err)
	workflows := map[string]prioritizedWorkflow{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		var workflow prioritizedWorkflow
		data := contractcheck.ReadRepoFile(t, root, filepath.Join(".github", "workflows", entry.Name()))
		contractcheck.FailErr(t, "decode "+entry.Name(), yaml.Unmarshal([]byte(data), &workflow))
		workflows[entry.Name()] = workflow
	}
	return workflows
}

func runnerPriorityClasses(t *testing.T) map[string][]string {
	t.Helper()
	var plan struct {
		RunnerPriority map[string][]string `json:"runner_priority"`
	}
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/verification-plan.json")
	contractcheck.FailErr(t, "decode verification plan", json.Unmarshal([]byte(data), &plan))
	return plan.RunnerPriority
}

// Every workflow that claims runners on its own trigger yields them by a declared class;
// CI's class follows its event, and reusable workflows run inside their callers.
func TestEveryTriggeredWorkflowDeclaresItsRunnerPriority(t *testing.T) {
	t.Parallel()
	workflows := prioritizedWorkflows(t)
	declared := map[string]string{}
	for class, files := range runnerPriorityClasses(t) {
		if !slices.Contains([]string{"release", "qualification", "warming", "background"}, class) {
			t.Errorf("unknown runner priority class %q", class)
		}
		for _, file := range files {
			if previous, ok := declared[file]; ok {
				t.Errorf("%s declares runner priorities %s and %s", file, previous, class)
			}
			declared[file] = class
			if _, ok := workflows[file]; !ok {
				t.Errorf("runner priority names missing workflow %s", file)
			}
		}
	}
	for name, workflow := range workflows {
		_, reusable := workflow.On["workflow_call"]
		owned := name == "ci.yml" || name == runnerScheduler || reusable && len(workflow.On) == 1
		if _, ok := declared[name]; ok == owned {
			t.Errorf("%s must declare a runner priority exactly when it is triggered outside CI and the scheduler", name)
		}
	}
}

// Merge groups and releases are never cancelled for newer work of their own kind.
func TestProtectedWorkflowsNeverCancelInProgress(t *testing.T) {
	t.Parallel()
	workflows := prioritizedWorkflows(t)
	for _, file := range runnerPriorityClasses(t)["release"] {
		if cancel := workflows[file].Concurrency.Cancel; cancel != "" && cancel != "false" {
			t.Errorf("%s must finish once started, got cancel-in-progress %q", file, cancel)
		}
	}
	if cancel := workflows["ci.yml"].Concurrency.Cancel; cancel != "${{ github.event_name == 'pull_request' }}" {
		t.Errorf("only a pull request's newer push may cancel its CI, got %q", cancel)
	}
}

func TestRunnerSchedulerSweepsAndReactsToProtectedDemand(t *testing.T) {
	t.Parallel()
	workflows := prioritizedWorkflows(t)
	scheduler := workflows[runnerScheduler]
	for _, event := range []string{"schedule", "workflow_run", "workflow_dispatch"} {
		if _, ok := scheduler.On[event]; !ok {
			t.Errorf("the runner scheduler must run on %s", event)
		}
	}
	var trigger struct {
		Workflows []string
		Types     []string
	}
	node := scheduler.On["workflow_run"]
	contractcheck.FailErr(t, "decode workflow_run trigger", node.Decode(&trigger))
	names := map[string]string{}
	for file, workflow := range workflows {
		names[workflow.Name] = file
	}
	for _, name := range trigger.Workflows {
		if _, ok := names[name]; !ok {
			t.Errorf("the runner scheduler waits on workflow %q, which does not exist", name)
		}
	}
	for _, required := range []string{workflows["ci.yml"].Name, workflows["release.yml"].Name, workflows["release-halt.yml"].Name} {
		if !slices.Contains(trigger.Workflows, required) {
			t.Errorf("the runner scheduler must react when %s claims runners", required)
		}
	}
	slices.Sort(trigger.Types)
	if !slices.Equal(trigger.Types, []string{"completed", "requested"}) {
		t.Errorf("the runner scheduler must react when demand arrives and when it leaves, got %v", trigger.Types)
	}
	want := map[string]string{"actions": "write", "contents": "read", "pull-requests": "read"}
	if len(scheduler.Permissions) != len(want) {
		t.Errorf("the runner scheduler needs exactly %v, got %v", want, scheduler.Permissions)
	}
	for scope, access := range want {
		if scheduler.Permissions[scope] != access {
			t.Errorf("the runner scheduler needs %s: %s, got %v", scope, access, scheduler.Permissions)
		}
	}
	if scheduler.Concurrency.Group == "" || scheduler.Concurrency.Cancel != "false" {
		t.Error("sweeps must run one at a time, and a running sweep must finish its cancellations and re-runs")
	}
	swept := false
	for _, node := range scheduler.Jobs {
		var job hostedJob
		contractcheck.FailErr(t, "decode runner scheduler job", node.Decode(&job))
		for _, step := range job.Steps {
			swept = swept || step.Run == "python3 scripts/ci_verification.py schedule" && job.Timeout != ""
		}
		// Ready pull requests outrank qualification and drafts, so their runs are demand too.
		if job.If != "" {
			t.Errorf("the runner scheduler must sweep on every triggering run, not filter on %q", job.If)
		}
	}
	if len(scheduler.Jobs) != 1 || !swept {
		t.Error("the runner scheduler must be one bounded job running the catalog's scheduling step")
	}
}
