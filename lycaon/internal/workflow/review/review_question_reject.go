package review

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

const submitVerdictQuestionInvalidCode = "SUBMIT_VERDICT_QUESTION_INVALID"

func rejectReviewQuestion(reason, id string) error {
	var subjects []string
	if id != "" {
		subjects = []string{id}
	}
	data := guidance.OffenderHintData(subjects)
	data["reason"] = reason
	data["question_id"] = id
	return &tools.ToolReject{Code: submitVerdictQuestionInvalidCode, Data: data}
}

// Follow-up requires an available investigation or review.
func checkQuestionContinuation(def workflowdef.ReviewLoopDef, claims []workflowvalidation.VerdictClaim, questions []reviewQuestionWork, tasks []api.WorkerTask, phase string) error {
	for _, claim := range claims {
		if def.ClassOf(claim.Status) != workflowdef.ClaimOpen {
			continue
		}
		for _, q := range questions {
			if q.ClaimID != claim.ID {
				continue
			}
			completed, active := questionAttempts(tasks, phase, q.ID)
			_, reviewing := questionAttempts(tasks, phase, q.ID+"/review")
			if active || reviewing || completed < def.FollowupAttempts || !questionReviewed(tasks, phase, q.ID, def.RequiredAgents) {
				return nil
			}
		}
	}
	return rejectReviewQuestion("investigations_exhausted", "")
}
