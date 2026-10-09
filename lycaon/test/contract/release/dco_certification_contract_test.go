package contract

import (
	"maps"
	"regexp"
	"slices"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestDCOCertificationUsesTrustedBoundedSerializedCaller(t *testing.T) {
	t.Parallel()
	var workflow struct {
		Permissions map[string]string
		Concurrency struct {
			Group  string
			Queue  string
			Cancel bool `yaml:"cancel-in-progress"`
		}
		Jobs map[string]struct {
			If          string
			RunsOn      string `yaml:"runs-on"`
			Timeout     int    `yaml:"timeout-minutes"`
			Permissions map[string]string
			Steps       []struct {
				Uses     string
				Run      string
				With     map[string]string
				Continue bool `yaml:"continue-on-error"`
			}
		}
	}
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/dco.yml")
	contractcheck.FailErr(t, "decode DCO caller", yaml.Unmarshal([]byte(data), &workflow))
	want := map[string]string{"contents": "read", "pull-requests": "read", "actions": "read", "checks": "write", "statuses": "write"}
	if !maps.Equal(workflow.Permissions, want) {
		t.Errorf("certification needs exactly %v, got %v", want, workflow.Permissions)
	}
	if workflow.Concurrency.Group != "dco-certification" || workflow.Concurrency.Queue != "max" || workflow.Concurrency.Cancel {
		t.Errorf("all DCO writers must serialize without cancelling or replacing pending events: %+v", workflow.Concurrency)
	}
	if len(workflow.Jobs) != 1 {
		t.Fatalf("certification must have one trusted publisher, got %d jobs", len(workflow.Jobs))
	}
	job, ok := workflow.Jobs["certify"]
	if !ok {
		t.Fatal("missing certification publisher")
	}
	if job.RunsOn != "ubuntu-24.04" || job.Timeout != 15 || len(job.Permissions) != 0 {
		t.Errorf("certification must use supported bounded runner and workflow permissions: %+v", job)
	}
	guard := "(github.event_name != 'pull_request_target' || (github.event.pull_request.draft == false && github.actor != 'dependabot[bot]')) && (github.event_name != 'workflow_run' || github.event.workflow_run.event == 'pull_request')"
	if job.If != guard {
		t.Errorf("draft events and non-PR CI runs must not enter privileged certification: %q", job.If)
	}
	if len(job.Steps) != 1 {
		t.Fatalf("publisher must execute only the shared action, got %d steps", len(job.Steps))
	}
	step := job.Steps[0]
	if !regexp.MustCompile(`^paintedwolf-ai/dco-checker@[0-9a-f]{40}$`).MatchString(step.Uses) || step.Run != "" || step.Continue || len(step.With) != 0 {
		t.Errorf("publisher must execute only an immutable shared checker with its reviewed defaults: %+v", step)
	}
	if !slices.Contains(runnerPriorityClasses(t)["one_shot"], "dco.yml") {
		t.Error("runner-priority sweeps must never cancel certification")
	}
}

func TestDCOCertificationRoutesReadyPRQueueFallbackAndManualEvents(t *testing.T) {
	t.Parallel()
	workflow := prioritizedWorkflows(t)["dco.yml"]
	wantEvents := []string{"merge_group", "pull_request_target", "workflow_dispatch", "workflow_run"}
	events := slices.Sorted(maps.Keys(workflow.On))
	if !slices.Equal(events, wantEvents) {
		t.Errorf("DCO event routes: got %v, want %v", events, wantEvents)
	}
	for event, want := range map[string][]string{
		"pull_request_target": {"edited", "opened", "ready_for_review", "reopened", "synchronize"},
		"merge_group":         {"checks_requested"}, "workflow_run": {"completed"},
	} {
		var trigger struct{ Types []string }
		node := workflow.On[event]
		contractcheck.FailErr(t, "decode "+event+" route", node.Decode(&trigger))
		slices.Sort(trigger.Types)
		if !slices.Equal(trigger.Types, want) {
			t.Errorf("%s must route %v, got %v", event, want, trigger.Types)
		}
	}
	var fallback struct{ Workflows []string }
	node := workflow.On["workflow_run"]
	contractcheck.FailErr(t, "decode trusted CI fallback", node.Decode(&fallback))
	if !slices.Equal(fallback.Workflows, []string{prioritizedWorkflows(t)["ci.yml"].Name}) {
		t.Errorf("fallback must follow the actual CI workflow: %v", fallback.Workflows)
	}
	var dispatch struct {
		Inputs map[string]struct {
			Required bool
			Type     string
		}
	}
	node = workflow.On["workflow_dispatch"]
	contractcheck.FailErr(t, "decode manual certification", node.Decode(&dispatch))
	input, ok := dispatch.Inputs["pull_request"]
	if !ok || len(dispatch.Inputs) != 1 || !input.Required || input.Type != "string" {
		t.Errorf("manual retry requires exactly a PR number input, never caller-selected commit evidence: %+v", dispatch.Inputs)
	}
}
