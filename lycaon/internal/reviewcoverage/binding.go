package reviewcoverage

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/lycaon/lycaon/pkg/api"
)

// Purpose determines the result required by a dispatched workflow task.
type Purpose string

const (
	IndependentReview     Purpose = "independent_review"
	QuestionInvestigation Purpose = "question_investigation"
	QuestionReview        Purpose = "question_review"
)

// Binding is immutable dispatch context. The job's terminal result is its assessment.
type Binding struct {
	JobStatus         string     `json:"-"`
	ID                string     `json:"id"`
	RunID             string     `json:"run_id"`
	Phase             string     `json:"phase"`
	WorkID            string     `json:"work_id"`
	Agent             string     `json:"agent"`
	Purpose           Purpose    `json:"purpose"`
	Subject           Assignment `json:"subject"`
	CoverageRequired  bool       `json:"coverage_required"`
	QuestionID        string     `json:"question_id,omitempty"`
	ClaimIDs          []string   `json:"claim_ids,omitempty"`
	PredecessorJobs   []string   `json:"predecessor_job_ids,omitempty"`
	InvestigationJobs []string   `json:"investigation_job_ids,omitempty"`
}

// Scoped retains precisely the registered obligations and gaps affecting them.
func Scoped(subject Assignment, obligations []string, claims ...string) Assignment {
	out := Assignment{Claims: map[string]string{}}
	for _, id := range claims {
		if revision, ok := subject.Claims[id]; ok {
			out.Claims[id] = revision
		}
	}
	for _, f := range subject.Facts.Obligations {
		if slices.Contains(obligations, f.ID) {
			out.Facts.Obligations = append(out.Facts.Obligations, f)
		}
	}
	for _, f := range subject.Facts.Gaps {
		affected := slices.Clone(f.Obligations)
		for _, a := range subject.Candidate.Assessments {
			if a.ID == f.ID {
				affected = append(affected, a.Obligations...)
			}
		}
		if slices.ContainsFunc(affected, func(id string) bool { return slices.Contains(obligations, id) }) {
			f.Obligations = slices.DeleteFunc(slices.Clone(f.Obligations), func(id string) bool { return !slices.Contains(obligations, id) })
			out.Facts.Gaps = append(out.Facts.Gaps, f)
		}
	}

	for _, a := range subject.Candidate.Assessments {
		if slices.ContainsFunc(out.Facts.Obligations, func(f Fact) bool { return f.ID == a.ID }) || slices.ContainsFunc(out.Facts.Gaps, func(f Fact) bool { return f.ID == a.ID }) {
			a.Obligations = slices.DeleteFunc(slices.Clone(a.Obligations), func(id string) bool { return !slices.Contains(obligations, id) })
			out.Candidate.Assessments = append(out.Candidate.Assessments, a)
		}
	}
	out.Facts.Seal()
	out.Candidate.Revision = out.Facts.Revision
	out.Facts.Revision = Identity(struct {
		Facts     Facts
		Candidate api.CoverageReview
		Claims    map[string]string
	}{out.Facts, out.Candidate, out.Claims})
	return out
}

// ChangedItems compares structured dependencies, independently of worker lifecycle.
func ChangedItems(assigned, current Assignment) []string {
	before, after := itemIdentities(assigned), itemIdentities(current)
	var changed []string
	for id, revision := range before {
		if after[id] != revision {
			changed = append(changed, id)
		}
	}
	for id := range after {
		if _, ok := before[id]; !ok {
			changed = append(changed, id)
		}
	}
	slices.Sort(changed)
	return changed
}

func itemIdentities(subject Assignment) map[string]string {
	out := map[string]string{}
	for id, revision := range subject.Claims {
		out["claim/"+id] = revision
	}
	candidates := map[string]api.CoverageAssessment{}
	for _, a := range subject.Candidate.Assessments {
		candidates[a.ID] = a
	}
	for _, rows := range [][]Fact{subject.Facts.Obligations, subject.Facts.Gaps} {
		for _, f := range rows {
			out[f.ID] = Identity(struct {
				Fact      Fact
				Candidate api.CoverageAssessment
			}{f, candidates[f.ID]})
		}
	}
	return out
}

// Compose replaces only explicit scoped items and refuses undeclared expansion.
func Compose(base api.CoverageReview, update api.CoverageReview, scope Facts) (api.CoverageReview, error) {
	if err := Validate(scope, update); err != nil {
		return api.CoverageReview{}, err
	}
	out := base
	out.Assessments = slices.Clone(base.Assessments)
	for _, assessment := range update.Assessments {
		i := slices.IndexFunc(out.Assessments, func(a api.CoverageAssessment) bool { return a.ID == assessment.ID })
		if i < 0 {
			return api.CoverageReview{}, fmt.Errorf("reassessment item %s has no predecessor", assessment.ID)
		}
		out.Assessments[i] = assessment
	}
	return out, nil
}

// Page projects bounded assessment inputs without changing the recorded revision.
func Page(subject Assignment, offset, limit int) (Assignment, string, error) {
	facts := append(append([]Fact{}, subject.Facts.Obligations...), subject.Facts.Gaps...)
	if offset < 0 || offset > len(facts) || limit < 1 {
		return Assignment{}, "", fmt.Errorf("invalid review subject page")
	}
	end := min(offset+limit, len(facts))
	page := Assignment{Facts: Facts{Revision: subject.Facts.Revision}, Candidate: api.CoverageReview{Revision: subject.Candidate.Revision, Assessments: []api.CoverageAssessment{}}}
	for i := offset; i < end; i++ {
		if i < len(subject.Facts.Obligations) {
			page.Facts.Obligations = append(page.Facts.Obligations, facts[i])
		} else {
			page.Facts.Gaps = append(page.Facts.Gaps, facts[i])
		}
		for _, a := range subject.Candidate.Assessments {
			if a.ID == facts[i].ID {
				page.Candidate.Assessments = append(page.Candidate.Assessments, a)
				break
			}
		}
	}
	next := ""
	if end < len(facts) {
		next = strconv.Itoa(end)
	}
	return page, next, nil
}

const SubjectPageSize = 50

type BindingPage struct {
	Binding
	NextCursor string `json:"next_cursor,omitempty"`
}

// FirstPage is the same bounded projection in initial context and repair feedback.
func FirstPage(binding Binding) BindingPage {
	page, next, _ := Page(binding.Subject, 0, SubjectPageSize)
	binding.Subject = page
	return BindingPage{Binding: binding, NextCursor: next}
}
