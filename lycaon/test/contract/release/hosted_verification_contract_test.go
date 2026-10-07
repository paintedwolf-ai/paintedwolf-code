package contract

import (
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

type hostedStep struct {
	Uses     string
	Run      string
	If       string
	Timeout  string `yaml:"timeout-minutes"`
	Continue bool   `yaml:"continue-on-error"`
	Env      map[string]string
}

type hostedJob struct {
	Uses     string
	Needs    yaml.Node
	If       string
	Timeout  string `yaml:"timeout-minutes"`
	Continue bool   `yaml:"continue-on-error"`
	With     map[string]string
	Steps    []hostedStep
	Strategy struct {
		FailFast *bool `yaml:"fail-fast"`
	}
}

type hostedWorkflow struct {
	On   map[string]yaml.Node
	Jobs map[string]hostedJob
}

func hostedWorkflowFile(t *testing.T, name string) hostedWorkflow {
	t.Helper()
	var workflow hostedWorkflow
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/"+name+".yml")
	contractcheck.FailErr(t, "decode hosted workflow", yaml.Unmarshal([]byte(data), &workflow))
	return workflow
}

func hostedJobs(t *testing.T, name string) map[string]hostedJob {
	t.Helper()
	return hostedWorkflowFile(t, name).Jobs
}

func hostedNeeds(t *testing.T, job hostedJob) []string {
	t.Helper()
	if job.Needs.Kind == yaml.ScalarNode {
		return []string{job.Needs.Value}
	}
	var needs []string
	contractcheck.FailErr(t, "decode job dependencies", job.Needs.Decode(&needs))
	return needs
}

func requireHostedGate(t *testing.T, jobs map[string]hostedJob, gate string, dependencies []string) {
	t.Helper()
	job := jobs[gate]
	got := hostedNeeds(t, job)
	slices.Sort(got)
	slices.Sort(dependencies)
	if !slices.Equal(got, dependencies) || job.If != "always()" {
		t.Fatalf("%s must always judge every dependency: got %v (%s), want %v", gate, got, job.If, dependencies)
	}
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Run, "python3 scripts/ci_verification.py gate") && step.Env["NEEDS_JSON"] == "${{ toJSON(needs) }}" {
			return
		}
	}
	t.Fatalf("%s does not require successful structured dependency results", gate)
}

func TestHostedVerificationAggregatesRequireEveryJob(t *testing.T) {
	t.Parallel()
	for workflow, gate := range map[string]string{"ci": "check", "nightly": "nightly", "verification": "verified"} {
		jobs := hostedJobs(t, workflow)
		var dependencies []string
		for name := range jobs {
			if name != gate {
				dependencies = append(dependencies, name)
			}
		}
		requireHostedGate(t, jobs, gate, dependencies)
	}
}

// Pull requests get the fast tier; main advances only through the merge queue,
// whose single required check judges the full tier on the commit that lands.
func TestCIRunsTheFastTierOnPullRequestsAndTheFullTierBeforeMain(t *testing.T) {
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
	if jobs["verification"].With["profile"] != "${{ github.event_name == 'pull_request' && 'fast' || 'check' }}" {
		t.Error("pull requests must run the fast profile and every other event the check profile")
	}
	if platform := jobs["platform"]; platform.Uses != "./.github/workflows/platform-verification.yml" ||
		platform.If != "github.event_name != 'pull_request'" {
		t.Error("platform verification must run in every full-tier event and only there")
	}
	requireHostedGate(t, jobs, "check", []string{"verification", "platform"})
	for _, step := range jobs["check"].Steps {
		if strings.HasPrefix(step.Run, "python3 scripts/ci_verification.py gate") &&
			(step.Env["SKIPPED"] != "${{ github.event_name == 'pull_request' && 'platform' || '' }}" ||
				!strings.Contains(step.Run, `--skipped "$SKIPPED"`)) {
			t.Error("the gate may excuse only the platform job, and only on pull requests")
		}
	}
}

// A release ships only a commit the full tier passed, and gates publication on
// its own preflight and upgrade rehearsal while the signed build runs alongside them.
func TestReleaseGatesPrecedePublication(t *testing.T) {
	t.Parallel()
	jobs := hostedJobs(t, "release")
	requireHostedGate(t, jobs, "ship-gates", []string{"classify", "preflight", "upgrade"})
	for name, dependencies := range map[string][]string{
		"preflight": {"classify"}, "upgrade": {"classify", "preflight"}, "build-release": {"classify"},
	} {
		if !slices.Equal(hostedNeeds(t, jobs[name]), dependencies) || jobs[name].If != "" {
			t.Errorf("%s must require successful %v before running", name, dependencies)
		}
	}
	verified := false
	for _, step := range jobs["classify"].Steps {
		verified = verified || step.Run == `python3 scripts/ci_verification.py verified "${GITHUB_SHA}"` &&
			step.If == "${{ github.ref_type == 'tag' }}"
	}
	if !verified {
		t.Error("a tagged release must prove its commit passed the full tier")
	}
	if !slices.Contains(hostedNeeds(t, jobs["publish-immutable"]), "ship-gates") {
		t.Error("publish-immutable must require ship-gates; signing may overlap the gates, publication may not")
	}
}

func TestHostedVerificationBudgetsAndEvidence(t *testing.T) {
	t.Parallel()
	jobs := hostedJobs(t, "verification")
	verify := jobs["verify"]
	if verify.Strategy.FailFast == nil || *verify.Strategy.FailFast {
		t.Fatal("one verification failure must not cancel independent matrix jobs")
	}
	if verify.Timeout != "${{ matrix.job_minutes }}" {
		t.Fatal("verification jobs must consume the catalog job budget")
	}
	var bounded, evidence bool
	for _, step := range verify.Steps {
		if strings.Contains(step.Run, "ci_verification.py run") {
			bounded = step.Timeout == "${{ matrix.minutes }}"
		}
		if step.Uses == "./.github/actions/verification-evidence" {
			evidence = step.If == "always()" && step.Timeout != ""
		}
	}
	if !bounded || !evidence {
		t.Fatal("verification needs an invocation deadline and always-run evidence collection")
	}
	for _, workflow := range []string{"ci", "nightly", "verification", "platform-verification", "desktop-verification"} {
		for name, job := range hostedJobs(t, workflow) {
			if job.Continue || job.Uses == "" && job.Timeout == "" {
				t.Errorf("%s/%s must be blocking and have an explicit deadline", workflow, name)
			}
			for _, step := range job.Steps {
				if step.Continue {
					t.Errorf("%s/%s must not suppress verification failures", workflow, name)
				}
			}
		}
	}
}

func TestDesktopVerificationHasOneImplementation(t *testing.T) {
	t.Parallel()
	for workflow, job := range map[string]string{"platform-verification": "playwright-desktop", "lycaon-den-nightly": "playwright-desktop"} {
		if hostedJobs(t, workflow)[job].Uses != "./.github/workflows/desktop-verification.yml" {
			t.Errorf("%s must use the shared desktop verification workflow", workflow)
		}
	}
}

func TestHostedProfilesAndSetupAreReachable(t *testing.T) {
	t.Parallel()
	for _, workflow := range []string{"ci", "nightly"} {
		if hostedJobs(t, workflow)["verification"].Uses != "./.github/workflows/verification.yml" {
			t.Errorf("%s must invoke catalog verification", workflow)
		}
	}
	if hostedJobs(t, "nightly")["verification"].With["profile"] != "nightly" {
		t.Error("nightly must invoke the nightly catalog profile")
	}
	for workflow, job := range map[string]string{"verification": "verify", "desktop-verification": "desktop", "release": "preflight"} {
		setup := false
		for _, step := range hostedJobs(t, workflow)[job].Steps {
			setup = setup || step.Uses == "./.github/actions/setup-verification"
			if step.Run != "" && !setup {
				t.Errorf("%s/%s must stage dependencies and notices before verification", workflow, job)
			}
		}
		if !setup {
			t.Errorf("%s/%s has no shared verification setup", workflow, job)
		}
	}
}
