package contract

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestDCOCertificationUsesTrustedBoundedSerializedCaller(t *testing.T) {
	t.Parallel()
	var workflow struct {
		RunName     string `yaml:"run-name"`
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
				Name     string
				If       string
				Uses     string
				Run      string
				With     map[string]string
				Env      map[string]string
				Continue bool `yaml:"continue-on-error"`
			}
		}
	}
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/dco.yml")
	contractcheck.FailErr(t, "decode DCO caller", yaml.Unmarshal([]byte(data), &workflow))
	wantRunName := "${{ inputs.pull_request == '' && inputs.queue_ref != '' && inputs.queue_sha != '' && format('DCO queue {0}', inputs.queue_sha) || 'Original commit certification' }}"
	if workflow.RunName != wantRunName {
		t.Errorf("certification run-name must identify only unambiguous queue dispatches: got %q, want %q", workflow.RunName, wantRunName)
	}
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
	guard := "(github.event_name != 'pull_request_target' || ((github.event.pull_request.draft == false || github.event.action == 'ready_for_review') && github.actor != 'dependabot[bot]')) && (github.event_name != 'workflow_run' || github.event.workflow_run.event == 'pull_request')"
	if job.If != guard {
		t.Errorf("draft and non-PR events must not enter privileged certification: %q", job.If)
	}
	if len(job.Steps) != 4 {
		t.Fatalf("publisher must have three default-branch queue recovery steps and the shared action step, got %d steps", len(job.Steps))
	}
	for _, step := range job.Steps {
		if step.Continue {
			t.Errorf("certification steps must never continue on error: %+v", step)
		}
	}
	queueIf := "github.event_name == 'workflow_dispatch' && (inputs.queue_ref != '' || inputs.queue_sha != '')"
	prIf := "github.event_name != 'workflow_dispatch' || (inputs.queue_ref == '' && inputs.queue_sha == '')"
	checkoutPattern := regexp.MustCompile(`^actions/checkout@[0-9a-f]{40}$`)
	actionPattern := regexp.MustCompile(`^paintedwolf-ai/dco-checker@([0-9a-f]{40})$`)
	match := actionPattern.FindStringSubmatch(job.Steps[3].Uses)
	if len(match) != 2 || job.Steps[3].If != prIf || job.Steps[3].Run != "" || len(job.Steps[3].With) != 0 {
		t.Fatalf("ordinary certification must execute only the immutable shared checker: %+v", job.Steps[3])
	}
	checkerSHA := match[1]
	if job.Steps[0].If != queueIf || !checkoutPattern.MatchString(job.Steps[0].Uses) || job.Steps[0].Run != "" ||
		!maps.Equal(job.Steps[0].With, map[string]string{"ref": "refs/heads/main", "persist-credentials": "false"}) {
		t.Errorf("queue certification must check out only the default-branch controller without persisting credentials: %+v", job.Steps[0])
	}
	if job.Steps[1].If != queueIf || !checkoutPattern.MatchString(job.Steps[1].Uses) || job.Steps[1].Run != "" ||
		!maps.Equal(job.Steps[1].With, map[string]string{
			"repository": "paintedwolf-ai/dco-checker", "ref": checkerSHA,
			"path": ".queue-dco-checker", "persist-credentials": "false",
		}) {
		t.Errorf("queue certification must check out the same immutable shared checker: %+v", job.Steps[1])
	}
	wantEnv := map[string]string{
		"GH_TOKEN":             "${{ github.token }}",
		"PYTHONPATH":           "scripts",
		"PULL_REQUEST":         "${{ inputs.pull_request }}",
		"QUEUE_REF":            "${{ inputs.queue_ref }}",
		"QUEUE_SHA":            "${{ inputs.queue_sha }}",
		"DCO_RUN_URL":          "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}",
		"DCO_RUN_ID":           "${{ github.run_id }}",
		"DCO_RUN_ATTEMPT":      "${{ github.run_attempt }}",
		"DCO_CALLER_REVISION":  "${{ github.workflow_sha }}",
		"DCO_CHECKER_REVISION": checkerSHA,
	}
	if job.Steps[2].If != queueIf || job.Steps[2].Uses != "" || len(job.Steps[2].With) != 0 ||
		job.Steps[2].Run != "python3 -m ci_policy.queue_certification .queue-dco-checker/scripts/dco_check.py" ||
		!maps.Equal(job.Steps[2].Env, wantEnv) {
		t.Errorf("queue certification must feed verified live queue context and full provenance into the unchanged checker: %+v", job.Steps[2])
	}
}

func TestDCOCertificationRoutesReadyPRQueueFallbackAndManualEvents(t *testing.T) {
	t.Parallel()
	var workflow struct{ On map[string]yaml.Node }
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/dco.yml")
	contractcheck.FailErr(t, "decode DCO event routes", yaml.Unmarshal([]byte(data), &workflow))
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
	contractcheck.FailErr(t, "decode completed-run fallback", node.Decode(&fallback))
	var ci struct{ Name string }
	data = contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/ci.yml")
	contractcheck.FailErr(t, "decode CI workflow identity", yaml.Unmarshal([]byte(data), &ci))
	if !slices.Equal(fallback.Workflows, []string{ci.Name}) {
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
	wantInputs := []string{"pull_request", "queue_ref", "queue_sha"}
	if got := slices.Sorted(maps.Keys(dispatch.Inputs)); !slices.Equal(got, wantInputs) {
		t.Errorf("manual certification must accept exactly %v, got %v", wantInputs, got)
	}
	for name, input := range dispatch.Inputs {
		if input.Required || input.Type != "string" {
			t.Errorf("manual input %s must be an optional string so PR and queue recovery routes stay distinct: %+v", name, input)
		}
	}
	// A token-created pull request or merge group fires no events of its own.
	maintenance := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/ci_policy/maintenance.py")
	if !strings.Contains(maintenance, "workflows/dco.yml/dispatches") ||
		!strings.Contains(maintenance, "'pull_request': str(pull['number'])") {
		t.Error("automation must dispatch certification for the pull request it creates")
	}
	queueDispatch := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/ci_policy/queue_dispatch.py")
	if !strings.Contains(queueDispatch, "workflows/dco.yml/dispatches") ||
		!strings.Contains(queueDispatch, "'queue_ref': ref, 'queue_sha': sha") {
		t.Error("queue recovery must dispatch DCO certification for active merge groups missing DCO-owned")
	}
}
