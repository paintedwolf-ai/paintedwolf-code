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
	Uses        string
	Needs       yaml.Node
	If          string
	Timeout     string `yaml:"timeout-minutes"`
	Continue    bool   `yaml:"continue-on-error"`
	With        map[string]string
	Permissions map[string]string
	Steps       []hostedStep
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
	if !slices.Equal(got, dependencies) || job.If != "${{ !cancelled() }}" {
		t.Fatalf("%s must judge every dependency unless the run is cancelled: got %v (%s), want %v", gate, got, job.If, dependencies)
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
	for workflow, gate := range map[string]string{"ci": "check", "nightly": "nightly"} {
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

func TestReusableVerificationFailsWithItsPlanOrAnyMatrixJob(t *testing.T) {
	t.Parallel()
	jobs := hostedJobs(t, "verification")
	if len(jobs) != 2 {
		t.Fatalf("reusable verification must contain only planning and execution, got %d jobs", len(jobs))
	}
	plan, planOK := jobs["plan"]
	verify, verifyOK := jobs["verify"]
	if !planOK || !verifyOK || plan.Continue || verify.Continue || verify.If != "" {
		t.Fatal("planning and every selected matrix job must contribute to the reusable workflow verdict")
	}
	if !slices.Equal(hostedNeeds(t, verify), []string{"plan"}) {
		t.Fatal("matrix execution must depend on successful planning")
	}
	if verify.Strategy.FailFast == nil || *verify.Strategy.FailFast {
		t.Fatal("each selected matrix job must run and retain its own evidence")
	}
	for _, step := range verify.Steps {
		if strings.HasPrefix(step.Run, "python3 scripts/ci_verification.py run") && step.Continue {
			t.Fatal("an unverified or failed lane must fail the reusable workflow")
		}
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

// A release ships only a commit the full tier passed and qualification passed, and
// gates publication on its own preflight, upgrade rehearsal, and release-time lanes
// while the signed build runs alongside them.
func TestReleaseGatesPrecedePublication(t *testing.T) {
	t.Parallel()
	jobs := hostedJobs(t, "release")
	requireHostedGate(t, jobs, "ship-gates", []string{"classify", "preflight", "upgrade", "release-checks", "qualification"})
	for name, dependencies := range map[string][]string{
		"preflight": {"classify"}, "upgrade": {"classify", "preflight"}, "build-release": {"classify"},
		"release-checks": {"classify"},
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
	if jobs["release-checks"].Uses != "./.github/workflows/verification.yml" || jobs["release-checks"].With["profile"] != "release" {
		t.Error("every release must run the release-time catalog profile")
	}
	found := false
	for _, step := range jobs["classify"].Steps {
		found = found || strings.Contains(step.Run, `python3 scripts/ci_verification.py qualified "${GITHUB_SHA}"`) && step.If == ""
	}
	qualification := jobs["qualification"]
	if !found || qualification.Uses != "./.github/workflows/qualification.yml" ||
		qualification.If != "needs.classify.outputs.qualified != 'true'" || qualification.Permissions["statuses"] != "write" {
		t.Error("a release must qualify its commit itself unless classification found a passing qualification")
	}
	var excused, required bool
	for _, step := range jobs["ship-gates"].Steps {
		excused = excused || strings.HasPrefix(step.Run, "python3 scripts/ci_verification.py gate") &&
			step.Env["SKIPPED"] == "${{ needs.classify.outputs.qualified == 'true' && 'qualification' || '' }}" &&
			strings.Contains(step.Run, `--skipped "$SKIPPED"`)
		required = required || step.Run == `python3 scripts/ci_verification.py qualified --require "${GITHUB_SHA}"` && step.If == ""
	}
	if !excused || !required {
		t.Error("ship-gates may excuse qualification only when it was reused, and must find the commit's latest qualification passed")
	}
}

// A complete qualification run records its verdict on the commit, and nightly
// qualifies main daily and each release candidate as it lands.
func TestQualificationRecordsAReusableVerdict(t *testing.T) {
	t.Parallel()
	jobs := hostedJobs(t, "qualification")
	if verification := jobs["verification"]; verification.Uses != "./.github/workflows/verification.yml" ||
		verification.With["profile"] != "qualification" || verification.With["suite"] != "${{ inputs.suite }}" {
		t.Error("qualification must run the selected suites of the qualification profile")
	}
	qualified := jobs["qualified"]
	if !slices.Equal(hostedNeeds(t, qualified), []string{"verification", "e2e"}) || qualified.If != "${{ !cancelled() }}" ||
		qualified.Permissions["statuses"] != "write" || len(jobs) != 3 {
		t.Fatal("the qualification verdict must judge every lane and be able to record itself")
	}
	recorded := false
	for _, step := range qualified.Steps {
		recorded = recorded || strings.HasPrefix(step.Run, "python3 scripts/ci_verification.py qualify") &&
			step.Env["NEEDS_JSON"] == "${{ toJSON(needs) }}" && step.Env["SUITE"] == "${{ inputs.suite }}" &&
			step.Env["COMMIT"] == "${{ github.sha }}"
	}
	if !recorded {
		t.Error("the qualification verdict must be recorded on the run's commit from structured job results")
	}

	nightly := hostedWorkflowFile(t, "nightly")
	var push struct {
		Branches []string
		Paths    []string
	}
	trigger := nightly.On["push"]
	contractcheck.FailErr(t, "decode nightly push trigger", trigger.Decode(&push))
	if !slices.Equal(push.Branches, []string{"main"}) || !slices.Equal(push.Paths, []string{"VERSION"}) {
		t.Error("nightly must qualify a release candidate as it lands on main")
	}
	if _, ok := nightly.On["schedule"]; !ok {
		t.Error("nightly must qualify main on a schedule")
	}
	if job := nightly.Jobs["qualification"]; job.Uses != "./.github/workflows/qualification.yml" ||
		job.With["suite"] != "${{ inputs.suite || 'all' }}" || job.Permissions["statuses"] != "write" {
		t.Error("nightly must run every qualification suite unless a dispatch selects one")
	}
	if job := nightly.Jobs["release-checks"]; job.Uses != "./.github/workflows/verification.yml" || job.With["profile"] != "release" {
		t.Error("nightly must run the release-time profile as an early warning")
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
	for _, workflow := range []string{"ci", "nightly", "qualification", "verification", "platform-verification", "e2e-verification"} {
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

func TestEndToEndVerificationRunsOnlyInSelectedQualification(t *testing.T) {
	t.Parallel()
	platform := hostedJobs(t, "platform-verification")
	if len(platform) != 2 || platform["confinement"].Timeout == "" || platform["upgrade-corpus"].Timeout == "" {
		t.Fatal("merge admission must retain confinement and upgrade gates without browser journeys")
	}
	e2e := hostedJobs(t, "e2e-verification")
	if len(e2e) != 2 || e2e["playwright-desktop"].Timeout == "" {
		t.Fatal("end-to-end verification must run web and desktop suites")
	}
	web := e2e["playwright-web"]
	if web.Strategy.FailFast == nil || *web.Strategy.FailFast || web.Continue {
		t.Fatal("each web shard must run independently and contribute to the verdict")
	}
	qualification := hostedJobs(t, "qualification")
	if qualification["e2e"].Uses != "./.github/workflows/e2e-verification.yml" ||
		qualification["e2e"].If != "inputs.suite == 'all' || inputs.suite == 'e2e'" {
		t.Fatal("complete and explicit E2E qualification runs must include browser verification")
	}
	const skipped = "${{ inputs.suite != 'all' && inputs.suite != 'e2e' && 'e2e' || '' }}"
	for _, step := range qualification["qualified"].Steps {
		if strings.HasPrefix(step.Run, "python3 scripts/ci_verification.py qualify") &&
			step.Env["SKIPPED"] == skipped && strings.Contains(step.Run, `--skipped "$SKIPPED"`) {
			return
		}
	}
	t.Fatal("qualification may excuse only deliberately unselected E2E; failures and unexpected skips must block")
}

func TestBrowserVerificationRetainsSuitesEvidenceAndCleanup(t *testing.T) {
	t.Parallel()
	workflow := hostedWorkflowFile(t, "e2e-verification")
	if len(workflow.On) != 1 {
		t.Fatal("browser verification must have one caller-owned schedule")
	}
	if _, ok := workflow.On["workflow_call"]; !ok {
		t.Fatal("browser verification must be a reusable workflow")
	}
	for name, command := range map[string]string{
		"playwright-web": "./task e2e:den", "playwright-desktop": "./task e2e:den:desktop",
	} {
		var tests, evidence, cleanup bool
		for _, step := range workflow.Jobs[name].Steps {
			tests = tests || step.Run == command && step.Timeout != "" && !step.Continue
			if step.Uses == "./.github/actions/verification-evidence" {
				evidence = tests && step.If == "always()" && step.Timeout != ""
			}
			if strings.Contains(step.Run, "./task e2e:cleanup") {
				cleanup = evidence && step.If == "always()" && step.Timeout != "" &&
					strings.Contains(step.Run, "ci_verification.py release")
			}
		}
		if !tests || !evidence || !cleanup {
			t.Errorf("%s must run its bounded suite, retain evidence, then release admission and clean up", name)
		}
	}
}

func TestHostedProfilesAndSetupAreReachable(t *testing.T) {
	t.Parallel()
	for _, workflow := range []string{"ci", "qualification"} {
		if hostedJobs(t, workflow)["verification"].Uses != "./.github/workflows/verification.yml" {
			t.Errorf("%s must invoke catalog verification", workflow)
		}
	}
	for workflow, job := range map[string]string{"verification": "verify", "e2e-verification": "playwright-desktop", "release": "preflight"} {
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
