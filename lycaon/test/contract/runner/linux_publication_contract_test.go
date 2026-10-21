package contract

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

type linuxPublicationStep struct {
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

func TestLinuxPublicationUsesProtectedAdmissionBeforeWriteCredentials(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var workflow struct {
		Jobs map[string]yaml.Node `yaml:"jobs"`
	}
	contractcheck.FailErr(t, "parse release workflow", yaml.Unmarshal([]byte(contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")), &workflow))
	var job struct {
		Needs []string               `yaml:"needs"`
		If    string                 `yaml:"if"`
		Steps []linuxPublicationStep `yaml:"steps"`
	}
	node := workflow.Jobs["publish-immutable"]
	contractcheck.FailErr(t, "decode immutable publication job", node.Decode(&job))
	if job.If != "${{ needs.classify.outputs.publish == 'true' }}" {
		t.Fatal("publication must retain its classified release gate")
	}
	for _, dependency := range job.Needs {
		if dependency == "linux-candidates" {
			t.Fatal("candidate-only Linux must not delay independent public platform publication")
		}
	}
	admission, policy := -1, -1
	for index, step := range job.Steps {
		switch step.ID {
		case "linux-policy":
			policy = index
			if step.With["ref"] != "${{ github.workflow_sha }}" || step.With["persist-credentials"] != "false" || step.With["path"] != ".linux-publication-controller" {
				t.Fatal("Linux admission must use protected main policy without persisted Git credentials")
			}
		case "linux-admission":
			admission = index
			if step.If != "needs.classify.outputs.linux_public == 'true'" || step.Uses != "./.linux-publication-controller/.github/actions/admit-linux-publication" {
				t.Fatal("public Linux must enter the protected admission action")
			}
			if step.Env["LINUX_REPOSITORY_ORIGIN"] != "${{ vars.LINUX_REPOSITORY_ORIGIN }}" {
				t.Fatal("repository origin must come from protected controller configuration")
			}
			if step.With["source_commit"] != "${{ needs.classify.outputs.source_sha }}" || step.With["campaign_run_id"] != "${{ inputs.linux_campaign_run_id }}" || step.With["publication_files"] != "${{ github.workspace }}/release-artifacts" {
				t.Fatal("Linux admission must bind the selected source, retained campaign and imminent files")
			}
		}
		for _, value := range step.Env {
			if strings.Contains(value, "secrets.CLOUDFLARE_") || strings.Contains(value, "secrets.R2_") {
				if admission < 0 || admission >= index {
					t.Fatal("production object credentials must follow Linux admission")
				}
			}
		}
	}
	if policy < 0 || admission <= policy {
		t.Fatal("protected policy checkout and admission must precede publication")
	}
}

func TestLinuxPublicationActionUsesManagedFreshAdmission(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var action struct {
		Runs struct {
			Steps []linuxPublicationStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	contractcheck.FailErr(t, "parse Linux admission action", yaml.Unmarshal([]byte(contractcheck.ReadRepoFile(t, root, ".github/actions/admit-linux-publication/action.yml")), &action))
	for _, step := range action.Runs.Steps {
		if step.ID != "admit-final-linux" {
			continue
		}
		for _, required := range []string{"./task release:publish:linux-admission --", "--campaign-run-id", "--source-commit", "--publication-files", "--retained-dir"} {
			if !strings.Contains(step.Run, required) {
				t.Fatalf("managed publication admission lacks %q", required)
			}
		}
		return
	}
	t.Fatal("protected action lacks fresh final admission")
}

func TestReleaseProductionEntriesStayOutsideVerificationTargets(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	taskfile := contractcheck.ReadRepoFile(t, root, "Taskfile.yml")
	var plan struct {
		Tasks     map[string][]string `json:"tasks"`
		Resources map[string]struct {
			Locks []string `json:"locks"`
		} `json:"resources"`
	}
	contractcheck.FailErr(t, "parse verification plan", json.Unmarshal([]byte(contractcheck.ReadRepoFile(t, root, "scripts/verification-plan.json")), &plan))
	entry := regexp.MustCompile(`\./task (release:[a-z:-]+)`)
	for _, path := range []string{".github/workflows/release.yml", ".github/actions/admit-linux-publication/action.yml"} {
		workflow := contractcheck.ReadRepoFile(t, root, path)
		for _, forbidden := range []string{"release:live-test", "release:rehearse:", "<<'PYTHON'", "python3 -", "python3 .linux-publication-controller"} {
			if strings.Contains(workflow, forbidden) {
				t.Fatalf("%s runs production publication through %q", path, forbidden)
			}
		}
		for _, match := range entry.FindAllStringSubmatch(workflow, -1) {
			name := match[1]
			if !strings.HasPrefix(name, "release:publish:") {
				continue
			}
			if !strings.Contains(taskfile, "\n  "+name+":\n") {
				t.Fatalf("%s calls undeclared production entry %s", path, name)
			}
			if _, verification := plan.Tasks[name]; verification {
				t.Fatalf("production entry %s must not be a verification target", name)
			}
		}
	}
	declaration := regexp.MustCompile(`(?m)^  (release:(?:publish|rehearse):[a-z-]+):\n(?:    .*\n)*?      - (.*)\n`)
	declared := declaration.FindAllStringSubmatch(taskfile, -1)
	if len(declared) == 0 {
		t.Fatal("Taskfile declares no release publication or rehearsal entries")
	}
	for _, match := range declared {
		name, command := match[1], match[2]
		if !strings.HasPrefix(command, "python3 -m scripts.linux_release.") {
			t.Fatalf("%s must invoke its module as scripts.linux_release, got %q", name, command)
		}
		if !strings.HasPrefix(name, "release:rehearse:") {
			continue
		}
		resource, registered := plan.Resources[name]
		if _, task := plan.Tasks[name]; !task || !registered || len(resource.Locks) == 0 {
			t.Fatalf("rehearsal %s must be a registered verification target with scoped locks", name)
		}
		for _, lock := range resource.Locks {
			if lock == "*" {
				t.Fatalf("rehearsal %s must not hold the exclusive lock", name)
			}
		}
	}
}
