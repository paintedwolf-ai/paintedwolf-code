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

func hostedJobs(t *testing.T, name string) map[string]hostedJob {
	t.Helper()
	var workflow struct {
		Jobs map[string]hostedJob
	}
	data := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), ".github/workflows/"+name+".yml")
	contractcheck.FailErr(t, "decode hosted workflow", yaml.Unmarshal([]byte(data), &workflow))
	return workflow.Jobs
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
		if step.Run == "python3 scripts/ci_verification.py gate" && step.Env["NEEDS_JSON"] == "${{ toJSON(needs) }}" {
			return
		}
	}
	t.Fatalf("%s does not require successful structured dependency results", gate)
}

func TestHostedVerificationAggregatesRequireEveryJob(t *testing.T) {
	t.Parallel()
	for workflow, gate := range map[string]string{"ci": "check", "nightly": "nightly", "verification": "verified", "lycaon-den": "e2e"} {
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

func TestReleaseVerificationPrecedesSigning(t *testing.T) {
	t.Parallel()
	jobs := hostedJobs(t, "release")
	requireHostedGate(t, jobs, "ship-gates", []string{"classify", "preflight", "verification", "desktop", "upgrade"})
	if jobs["verification"].Uses != "./.github/workflows/verification.yml" || jobs["verification"].With["profile"] != "release" {
		t.Fatal("release must run the full catalog release profile")
	}
	for name, dependencies := range map[string][]string{
		"preflight": {"classify"}, "verification": {"preflight"}, "desktop": {"preflight"},
		"upgrade": {"classify", "preflight"}, "build-release": {"classify", "ship-gates"},
	} {
		if !slices.Equal(hostedNeeds(t, jobs[name]), dependencies) || jobs[name].If != "" {
			t.Errorf("%s must require successful %v before running", name, dependencies)
		}
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
	for _, workflow := range []string{"ci", "nightly", "verification", "desktop-verification"} {
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
	for workflow, job := range map[string]string{"lycaon-den": "playwright-desktop", "lycaon-den-nightly": "playwright-desktop", "release": "desktop"} {
		if hostedJobs(t, workflow)[job].Uses != "./.github/workflows/desktop-verification.yml" {
			t.Errorf("%s must use the shared desktop verification workflow", workflow)
		}
	}
}

func TestHostedProfilesAndSetupAreReachable(t *testing.T) {
	t.Parallel()
	for workflow, profile := range map[string]string{"ci": "check", "nightly": "nightly", "release": "release"} {
		job := hostedJobs(t, workflow)["verification"]
		if job.Uses != "./.github/workflows/verification.yml" || job.With["profile"] != profile {
			t.Errorf("%s must invoke its catalog verification profile %s", workflow, profile)
		}
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
