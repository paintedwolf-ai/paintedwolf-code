package contract

import (
	"strings"
	"testing"
)

func TestQualificationCheckoutsBindSourceAndControllerSeparately(t *testing.T) {
	t.Parallel()
	for _, workflow := range []string{"verification", "platform-verification", "e2e-verification"} {
		for job, spec := range hostedJobs(t, workflow) {
			for _, step := range spec.Steps {
				if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["ref"] != "${{ inputs.source-sha || github.sha }}" {
					t.Errorf("%s/%s does not check out its selected qualification source", workflow, job)
				}
			}
		}
	}
	for _, job := range []string{"source", "qualification"} {
		found := false
		for _, step := range hostedJobs(t, "qualification")[job].Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") {
				found = true
				if step.With["ref"] != "${{ github.workflow_sha }}" {
					t.Errorf("%s does not use the trusted qualification controller", job)
				}
			}
		}
		if !found {
			t.Errorf("%s has no controller checkout", job)
		}
	}
}

func TestQualificationPublishesOnlyTheCompletedAttemptReceipt(t *testing.T) {
	t.Parallel()
	job := hostedJobs(t, "qualification")["qualification"]
	if job.If != "${{ !cancelled() }}" {
		t.Fatal("the aggregate must judge every stage unless qualification is cancelled")
	}
	verified, published := false, false
	for _, step := range job.Steps {
		if step.Run == "python3 -m ci_policy.qualification_receipt" {
			verified = step.Env["QUALIFICATION_RECORD"] == "${{ needs.source.outputs.record }}" &&
				step.Env["NEEDS_JSON"] == "${{ toJSON(needs) }}"
		}
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			published = true
			if !verified || step.If != "" || step.Continue || step.With["name"] != "qualification-source-${{ github.run_attempt }}" ||
				step.With["path"] != "qualification-source.json" || step.With["if-no-files-found"] != "error" {
				t.Fatal("qualification must publish its exact attempt receipt only after every stage passes")
			}
		}
	}
	if !published {
		t.Fatal("qualification must retain source-bound evidence")
	}
}
