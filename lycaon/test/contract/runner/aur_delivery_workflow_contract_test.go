package contract

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

type workflowStep struct {
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type workflowJob struct {
	Needs yaml.Node      `yaml:"needs"`
	Steps []workflowStep `yaml:"steps"`
}

func parseWorkflowJobs(t *testing.T, root, path string) map[string]workflowJob {
	t.Helper()
	var workflow struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	contractcheck.FailErr(t, "parse "+path, yaml.Unmarshal([]byte(contractcheck.ReadRepoFile(t, root, path)), &workflow))
	return workflow.Jobs
}

func (job workflowJob) needs() []string {
	if job.Needs.Kind == yaml.ScalarNode {
		return []string{job.Needs.Value}
	}
	var names []string
	for _, node := range job.Needs.Content {
		names = append(names, node.Value)
	}
	return names
}

func (job workflowJob) artifactNames(action string) []string {
	var names []string
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/"+action+"@") {
			names = append(names, step.With["name"])
		}
	}
	return names
}

var runDownloadName = regexp.MustCompile(`gh run download "\$RELEASE_RUN_ID"[^\n]*?--name ([A-Za-z0-9._-]+)`)

// The AUR recipe job consumes only the bundle its own producer job retained in the same
// run, and that producer reads only an artifact the protected release controller uploads.
func TestAurDeliveryInputsHaveTheirProducerJob(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	delivery := parseWorkflowJobs(t, root, ".github/workflows/release-aur-delivery.yml")
	producer, consumer := delivery["inputs"], delivery["recipe"]
	produced := producer.artifactNames("upload-artifact")
	consumed := consumer.artifactNames("download-artifact")
	if len(produced) != 1 || len(consumed) != 1 || produced[0] != consumed[0] || produced[0] == "" {
		t.Fatalf("AUR input producer uploads %q but the recipe job downloads %q", produced, consumed)
	}
	if !slices.Contains(consumer.needs(), "inputs") {
		t.Fatalf("recipe job needs %q, want the inputs producer", consumer.needs())
	}
	for _, step := range consumer.Steps {
		if strings.Contains(step.Run, "gh run download") || step.With["run-id"] != "" {
			t.Fatal("recipe job must receive its inputs from this run's producer job, not another run")
		}
	}

	var fromController []string
	for _, step := range producer.Steps {
		script := strings.ReplaceAll(step.Run, "\\\n", " ")
		for _, match := range runDownloadName.FindAllStringSubmatch(script, -1) {
			fromController = append(fromController, match[1])
		}
	}
	controller := parseWorkflowJobs(t, root, ".github/workflows/release.yml")
	var uploaded []string
	for _, job := range controller {
		uploaded = append(uploaded, job.artifactNames("upload-artifact")...)
	}
	if len(fromController) == 0 {
		t.Fatal("AUR input producer reads no controller artifact")
	}
	for _, name := range fromController {
		if !slices.Contains(uploaded, name) {
			t.Fatalf("AUR input producer downloads %q, which release.yml never uploads", name)
		}
	}
}
