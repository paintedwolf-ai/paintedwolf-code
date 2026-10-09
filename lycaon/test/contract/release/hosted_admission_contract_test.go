package contract

import (
	"maps"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

const ciGateStep = "python3 scripts/ci_verification.py gate"

// A ready pull request runs the fast tier; the merge queue runs the integration gate on
// the commit that lands; a dispatch runs either tier by hand. Each reports the required check.
func TestReadyPullRequestsRunTheFastTierAndMergeGroupsTheGate(t *testing.T) {
	t.Parallel()
	workflow := hostedWorkflowFile(t, "ci")
	for _, event := range []string{"pull_request", "merge_group", "workflow_dispatch"} {
		if _, ok := workflow.On[event]; !ok {
			t.Errorf("CI must run on %s", event)
		}
	}
	if _, ok := workflow.On["push"]; ok {
		t.Error("the merge queue verifies what lands on main; a push run would repeat it")
	}
	if workflow.Concurrency.Cancel.Value != "${{ github.event_name == 'pull_request' }}" {
		t.Errorf("only a pull request's newer push may cancel its CI, got %q", workflow.Concurrency.Cancel.Value)
	}
	var pullRequest struct {
		Paths       []string
		PathsIgnore []string `yaml:"paths-ignore"`
	}
	trigger := workflow.On["pull_request"]
	contractcheck.FailErr(t, "decode pull_request trigger", trigger.Decode(&pullRequest))
	if len(pullRequest.Paths)+len(pullRequest.PathsIgnore) > 0 {
		t.Error("the required check must report on every pull request")
	}
	jobs := workflow.Jobs
	fast, integration, platform := jobs["fast"], jobs["integration"], jobs["platform"]
	if fast.With["profile"] != "fast" ||
		fast.If != "${{ github.event_name == 'pull_request' && !github.event.pull_request.draft || inputs.profile == 'fast' }}" {
		t.Error("ready pull requests, and only they or a dispatch asking for it, run the fast tier")
	}
	if integration.With["profile"] != "${{ github.event_name == 'merge_group' && 'integration' || 'check' }}" ||
		integration.If != "${{ github.event_name == 'merge_group' || inputs.profile == 'check' }}" {
		t.Error("merge groups run the integration gate, and a full dispatch the check profile")
	}
	if platform.Uses != "./.github/workflows/platform-verification.yml" || platform.If != "inputs.profile == 'check'" {
		t.Error("a full dispatch must include platform qualification")
	}
	requireHostedGate(t, jobs, "check", []string{"fast", "integration", "platform"})
	const skipped = "${{ inputs.profile == 'check' && 'fast' || format('{0},platform', github.event_name == 'merge_group' && 'fast' || 'integration') }}"
	for _, step := range jobs["check"].Steps {
		if strings.HasPrefix(step.Run, ciGateStep) && (step.Env["SKIPPED"] != skipped || !strings.Contains(step.Run, `--skipped "$SKIPPED"`)) {
			t.Error("each event may excuse only the tiers it does not run")
		}
	}
}

// Drafts spend no verification runners, never pass the required check, and get
// the fast tier once marked ready.
func TestDraftPullRequestsWaitForReadyForReview(t *testing.T) {
	t.Parallel()
	workflow := hostedWorkflowFile(t, "ci")
	var pullRequest struct{ Types []string }
	trigger := workflow.On["pull_request"]
	contractcheck.FailErr(t, "decode pull_request trigger", trigger.Decode(&pullRequest))
	types := slices.Clone(pullRequest.Types)
	slices.Sort(types)
	if !slices.Equal(types, []string{"opened", "ready_for_review", "reopened", "synchronize"}) {
		t.Errorf("pull request CI must run when a ready pull request changes and when a draft becomes ready, got %v", pullRequest.Types)
	}
	if !strings.Contains(workflow.Jobs["fast"].If, "!github.event.pull_request.draft") {
		t.Error("draft pull requests must not start verification")
	}
	refused := false
	for _, step := range workflow.Jobs["check"].Steps {
		refused = refused || strings.HasPrefix(step.Run, ciGateStep) &&
			step.Env["DRAFT"] == "${{ github.event.pull_request.draft == true }}" && strings.Contains(step.Run, `--draft "$DRAFT"`)
	}
	if !refused {
		t.Error("the required check must refuse a draft rather than pass on skipped verification")
	}
}

// A merge group's jobs watch their own queue branch and cancel the run once the queue
// removes the group. Only the merge-group caller may grant the write that cancelling needs.
func TestMergeGroupJobsStopWithTheirRemovedGroup(t *testing.T) {
	t.Parallel()
	ci := hostedWorkflowFile(t, "ci")
	if ci.Permissions["actions"] != "" || ci.Jobs["fast"].Permissions != nil {
		t.Error("pull request CI runs unreviewed code and must not hold actions write")
	}
	if want := map[string]string{"contents": "read", "actions": "write"}; !maps.Equal(ci.Jobs["integration"].Permissions, want) {
		t.Errorf("merge-group CI needs exactly %v to cancel its own run, got %v", want, ci.Jobs["integration"].Permissions)
	}
	if verification := hostedWorkflowFile(t, "verification"); verification.Permissions != nil {
		t.Error("reusable verification must inherit its caller's permissions, or merge groups could not cancel")
	}
	checkedOut, watched := false, false
	for _, step := range hostedJobs(t, "verification")["verify"].Steps {
		checkedOut = checkedOut || strings.HasPrefix(step.Uses, "actions/checkout@")
		if step.Uses == "./.github/actions/merge-group-watch" {
			watched = checkedOut
		}
		if step.Uses == "./.github/actions/setup-verification" && !watched {
			t.Error("each verification job must start watching its merge group right after checkout")
		}
	}
	var action struct {
		Runs struct {
			Steps []hostedStep
		}
	}
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/actions/merge-group-watch/action.yml")
	contractcheck.FailErr(t, "decode merge-group watch", yaml.Unmarshal([]byte(data), &action))
	steps := action.Runs.Steps
	if len(steps) != 1 || steps[0].If != "github.event_name == 'merge_group'" ||
		!strings.Contains(steps[0].Run, "nohup python3 -m ci_policy.merge_group") || !strings.HasSuffix(strings.TrimSpace(steps[0].Run), "&") {
		t.Error("the watch must run in the background for the rest of a merge-group job, and only there")
	}
}

// Speculative groups of one pull request each build in parallel, so a failure costs one
// rebuild behind it and never sends a batch back; every group must pass in full.
func TestMergeQueueBuildsParallelGroupsOfOne(t *testing.T) {
	t.Parallel()
	settings := readQueueSettings(t, contractcheck.RepoRoot(t))
	for key, want := range map[string]any{
		"merge_method": "SQUASH", "min_entries_to_merge": 1.0, "max_entries_to_merge": 1.0,
		"min_entries_to_merge_wait_minutes": 0.0, "grouping_strategy": "ALLGREEN",
	} {
		if settings[key] != want {
			t.Errorf("merge queue %s must be %v, got %v", key, want, settings[key])
		}
	}
	if groups, _ := settings["max_entries_to_build"].(float64); groups < 2 || groups > 3 {
		t.Errorf("the merge queue must build two or three groups at once, got %v", settings["max_entries_to_build"])
	}
	if timeout, _ := settings["check_response_timeout_minutes"].(float64); timeout < 60 {
		t.Errorf("a capped full-scope gate needs at least an hour to report, got %v minutes", settings["check_response_timeout_minutes"])
	}
}
