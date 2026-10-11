package runstate

import (
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// AcceptedReviewInputs retains the evidence used to accept the final assessment.
// Worker results remain authoritative; these are immutable report projections.
type AcceptedReviewInputs struct {
	Facts      reviewcoverage.Facts `json:"facts"`
	Snapshot   ReviewSnapshot       `json:"snapshot"`
	ResultJobs []string             `json:"result_jobs"`
}

func AcceptedReviewInputsFromVars(vars map[string]any, manifest workflowdef.Manifest) (*AcceptedReviewInputs, error) {
	last := ""
	for _, phase := range manifest.PhaseDefs {
		if phase.ReviewLoop != nil && phase.ReviewLoop.CarriesCoverage() {
			last = phase.ID
		}
	}
	if last == "" {
		return nil, nil
	}
	value, ok := conditions.DotPathGet(vars, "accepted_review_subjects."+last)
	if !ok {
		return nil, nil
	}
	raw, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("accepted review inputs are invalid")
	}
	var out AcceptedReviewInputs
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

const ReviewContextChangedCode = "SUBMIT_VERDICT_REVIEW_CONTEXT_CHANGED"
