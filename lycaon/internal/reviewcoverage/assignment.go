package reviewcoverage

import (
	"encoding/json"
	"github.com/lycaon/lycaon/pkg/api"
)

// Assignment binds an independent assessment to its scope and candidate judgment.
type Assignment struct {
	Claims    map[string]string  `json:"claims,omitempty"`
	Facts     Facts              `json:"facts"`
	Candidate api.CoverageReview `json:"candidate"`
}

func Assign(facts Facts, candidate api.CoverageReview, phase string) Assignment {
	raw, err := json.Marshal(Assignment{Facts: facts, Candidate: candidate})
	if err != nil {
		panic(err)
	}
	var copy Assignment
	if err := json.Unmarshal(raw, &copy); err != nil {
		panic(err)
	}
	facts, candidate = copy.Facts, copy.Candidate
	subject := Facts{Obligations: facts.Obligations}
	for _, gap := range facts.Gaps {
		// Question freshness and reviewer completion have their own workflow gates.
		if gap.Kind != "review_question" && !((gap.Kind == "supporting_work" || gap.Kind == "worker_scope") && gap.Phase == phase) {
			subject.Gaps = append(subject.Gaps, gap)
		}
	}
	subject.Seal()
	subject.Revision = Identity(struct {
		Scope     string
		Candidate api.CoverageReview
	}{subject.Revision, candidate})
	return Assignment{Facts: subject, Candidate: candidate}
}
