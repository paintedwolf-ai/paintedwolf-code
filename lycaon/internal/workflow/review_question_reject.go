package workflow

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

const submitVerdictQuestionInvalidCode = "SUBMIT_VERDICT_QUESTION_INVALID"

func rejectReviewQuestion(reason, id string) error {
	switch reason {
	case "current_review_required":
		return &tools.ToolReject{Code: ReviewRequiredCode, Data: map[string]any{"action": "dispatch_work", "work_ids": []string{questionReviewWorkID(id)}, "question_id": id}}
	case "investigation_required":
		return &tools.ToolReject{Code: ReviewRequiredCode, Data: map[string]any{"action": "dispatch_work", "work_ids": []string{id}, "question_id": id}}
	case "work_active":
		return &tools.ToolReject{Code: ReviewRequiredCode, Data: map[string]any{"action": "wait_for_work", "work_ids": []string{id, questionReviewWorkID(id)}, "question_id": id}}
	}
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
func checkQuestionContinuation(def workflowdef.ReviewLoopDef, claims []VerdictClaim, questions []reviewQuestionWork, tasks []api.WorkerTask, phase string) error {
	for _, claim := range claims {
		if def.ClassOf(claim.Status) != workflowdef.ClaimOpen {
			continue
		}
		for _, q := range questions {
			if q.ClaimID != claim.ID {
				continue
			}
			completed, active := questionAttempts(tasks, phase, q.ID)
			_, reviewing := questionAttempts(tasks, phase, questionReviewWorkID(q.ID))
			if active || reviewing || completed < def.FollowupAttempts || !questionReviewed(tasks, phase, q.ID, def.RequiredAgents) {
				return nil
			}
		}
	}
	return rejectReviewQuestion("investigations_exhausted", "")
}
