package reviewcoverage

import "github.com/lycaon/lycaon/pkg/api"

// Assignment binds an independent assessment to its scope and candidate judgment.
type Assignment struct {
	Facts     Facts              `json:"facts"`
	Candidate api.CoverageReview `json:"candidate"`
}

func Assign(facts Facts, candidate api.CoverageReview, phase string) Assignment {
	subject := Facts{Obligations: facts.Obligations}
	for _, gap := range facts.Gaps {
		// Question freshness and reviewer completion have their own workflow gates.
		if gap.Kind != "review_question" && !(gap.Kind == "supporting_work" && gap.Phase == phase) {
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
