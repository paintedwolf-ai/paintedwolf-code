package contract

import (
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
		for _, required := range []string{"./task release:live-test -- --admit-linux-publication", "--campaign-run-id", "--source-commit", "--publication-files", "--retained-dir"} {
			if !strings.Contains(step.Run, required) {
				t.Fatalf("managed publication admission lacks %q", required)
			}
		}
		return
	}
	t.Fatal("protected action lacks fresh final admission")
}
