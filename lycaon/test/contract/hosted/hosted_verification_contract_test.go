package contract

import (
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

type hostedStep struct {
	ID       string `yaml:"id"`
	Name     string
	Uses     string
	Run      string
	If       string
	Timeout  string `yaml:"timeout-minutes"`
	Continue bool   `yaml:"continue-on-error"`
	Env      map[string]string
	With     map[string]string
}

type hostedJob struct {
	Name        string
	Uses        string
	Permissions map[string]string
	Needs       yaml.Node
	If          string
	Timeout     string `yaml:"timeout-minutes"`
	Continue    bool   `yaml:"continue-on-error"`
	With        map[string]string
	Steps       []hostedStep
	Strategy    struct {
		FailFast string `yaml:"fail-fast"`
	}
}

type hostedWorkflow struct {
	Permissions map[string]string
	Concurrency struct {
		Group  string
		Cancel yaml.Node `yaml:"cancel-in-progress"`
	}
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
	expectedCondition := "${{ !cancelled() }}"
	if gate == "check" {
		expectedCondition = "${{ !cancelled() && inputs.lane == '' && !(github.event_name == 'pull_request' && github.event.pull_request.draft) }}"
	}
	if gate == "ship-gates" {
		expectedCondition = "${{ !cancelled() && inputs.rehearsal_run_id == '' }}"
	}
	if !slices.Equal(got, dependencies) || job.If != expectedCondition {
		t.Fatalf("%s must judge every dependency unless the run is cancelled: got %v (%s), want %v", gate, got, job.If, dependencies)
	}
	for _, step := range job.Steps {
		command := "python3 scripts/ci_verification.py gate"
		if gate == "qualification" {
			command = "python3 -m ci_policy.qualification_receipt"
		}
		if strings.HasPrefix(step.Run, command) && step.Env["NEEDS_JSON"] == "${{ toJSON(needs) }}" {
			return
		}
	}
	t.Fatalf("%s does not require successful structured dependency results", gate)
}

func TestHostedVerificationAggregatesRequireEveryBlockingJob(t *testing.T) {
	t.Parallel()
	// A job that observes, or reports the gate's own result, is not a dependency.
	for workflow, gate := range map[string]string{"ci": "check", "nightly": "nightly", "qualification": "qualification"} {
		jobs := hostedJobs(t, workflow)
		var dependencies []string
		for name := range jobs {
			if name != gate && !(workflow == "nightly" && name == "quarantine") &&
				!(workflow == "ci" && name == "dispatched-check") {
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
	// One failed run reports every failing lane, so repairs need not rediscover them a cycle at a time.
	if verify.Strategy.FailFast != "false" {
		t.Fatalf("no run may stop its other lanes at the first failure, got fail-fast %q", verify.Strategy.FailFast)
	}
	for _, step := range verify.Steps {
		if strings.HasPrefix(step.Run, "python3 scripts/ci_verification.py run") && step.Continue {
			t.Fatal("an unverified or failed lane must fail the reusable workflow")
		}
	}
}

// Publication requires qualified source, preflight, and upgrade rehearsal.
func TestReleaseGatesPrecedePublication(t *testing.T) {
	t.Parallel()
	jobs := hostedJobs(t, "release")
	requireHostedGate(t, jobs, "ship-gates", []string{"classify", "validate-source"})
	for name, dependencies := range map[string][]string{
		"compatibility": {"classify"}, "validate-source": {"classify", "compatibility"},
		"build-release": {"classify", "compatibility"},
	} {
		if !slices.Equal(hostedNeeds(t, jobs[name]), dependencies) || jobs[name].If != "" {
			t.Errorf("%s must require successful %v before running", name, dependencies)
		}
	}
	verified := false
	for _, step := range jobs["classify"].Steps {
		qualification := strings.Index(step.Run, `python3 scripts/ci_verification.py verified "${source_sha}"`)
		publication := strings.Index(step.Run, "publish=true")
		verified = verified || qualification >= 0 && publication > qualification &&
			step.Env["GH_TOKEN"] == "${{ github.token }}"
	}
	if !verified {
		t.Error("a tagged release must prove its commit passed the full tier")
	}
	if !slices.Contains(hostedNeeds(t, jobs["publish-immutable"]), "ship-gates") {
		t.Error("publish-immutable must require ship-gates; signing may overlap the gates, publication may not")
	}
}
