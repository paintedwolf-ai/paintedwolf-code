package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The verification matrix derives linux_launch from a lane's targets. A job
// that runs the Linux confinement target directly declares the protected
// launch's GTK and display provisioning itself, or its launch leg fails on
// the GTK compiler contract.
func TestDirectConfinementJobsProvisionLaunchDependencies(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	contractcheck.FailErr(t, "list hosted workflows", err)
	checked := 0
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".yml")
		if !ok || !strings.Contains(contractcheck.ReadRepoFile(t, root, ".github/workflows/"+entry.Name()), "test:confine:linux") {
			continue
		}
		for job, definition := range hostedWorkflowFile(t, name).Jobs {
			if !runsConfinementDirectly(definition) {
				continue
			}
			checked++
			if !provisionsLinuxLaunch(definition) {
				t.Errorf("%s job %s runs test:confine:linux without setup-verification linux-launch", name, job)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no hosted job runs ./task test:confine:linux directly")
	}
}

func runsConfinementDirectly(job hostedJob) bool {
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "test:confine:linux") {
			return true
		}
	}
	return false
}

func provisionsLinuxLaunch(job hostedJob) bool {
	for _, step := range job.Steps {
		if step.Uses == "./.github/actions/setup-verification" && (step.With["linux-launch"] == "true" || step.With["setup"] == "shell") {
			return true
		}
	}
	return false
}
