package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// RegisterAnswerDecisionTool registers worker decision answers.
func RegisterAnswerDecisionTool(reg *tools.DefaultRegistry, deps AnswerDecisionToolDeps) error {
	if reg == nil || deps.Answer == nil {
		return fmt.Errorf("registry and answer decision service required")
	}
	svc := deps.Answer
	return reg.Register("answer_decision", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		jobID, _ := args["job_id"].(string)
		option, _ := args["option"].(string)
		resolvedBy, _ := args["resolved_by"].(string)
		captureWorkerSubject(tctx, svc.Queue, jobID)
		out, err := svc.AnswerJob(ctx, strings.TrimSpace(tctx.Identity.SessionID), strings.TrimSpace(jobID), strings.TrimSpace(option), strings.TrimSpace(resolvedBy))
		if err != nil {
			return "", err
		}
		if tctx.Effects.Out != nil {
			subject := tctx.Effects.Out.DisplaySubject
			if subject != "" {
				subject += " · "
			}
			tctx.SetDisplaySubject(subject + out.Option)
		}
		raw, _ := json.Marshal(map[string]any{
			"status":      "resumed",
			"job_id":      out.JobID,
			"option":      out.Option,
			"resolved_by": out.ResolvedBy,
		})
		return string(raw), nil
	})
}

// AnswerDecisionToolDeps wires the coordinator answer_decision tool.
type AnswerDecisionToolDeps struct {
	Answer *AnswerDecisionService
}
