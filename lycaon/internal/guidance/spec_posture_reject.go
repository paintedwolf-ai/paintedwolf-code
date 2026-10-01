package guidance

import (
	"context"
	"fmt"
	"strings"
)

// RenderSpecPostureRejectBlock renders reject/spec-posture-block.md for spec posture tool denies.
func RenderSpecPostureRejectBlock(
	ctx context.Context,
	sessionID string,
	attemptedTool string,
	out any,
	progress PlanProgress,
	copy map[string]string,
	formatter *ToolRejectFormatter,
) (string, error) {
	o, ok := out.(interface {
		GetRejectCode() string
		GetPhaseRequired() string
		GetPhaseRequiredName() string
		GetMinRequired() string
		GetMaxPlaybook() string
	})
	if !ok {
		return "", fmt.Errorf("unsupported outcome type %T", out)
	}

	rejectCode := strings.TrimSpace(o.GetRejectCode())
	if rejectCode == "" {
		return "", fmt.Errorf("missing reject code")
	}

	phaseReq := strings.TrimSpace(o.GetPhaseRequired())
	phaseName := strings.TrimSpace(o.GetPhaseRequiredName())

	if copy == nil {
		return "", fmt.Errorf("phase rejection requires evaluated policy copy")
	}
	required := strings.TrimSpace(o.GetMinRequired())
	if max := strings.TrimSpace(o.GetMaxPlaybook()); max != "" {
		required = strings.TrimSpace(required + " (" + max + ")")
	}
	progressCompact := formatProgressCompact(progress, phaseReq)

	details := ""
	if progress.ProgressChecklist != "" && formatter.shouldAppendDetails(sessionID, progress.ChecklistHash) {
		details = strings.TrimSpace(progress.ProgressChecklist)
	}

	block, err := RenderGuidance(ctx, "reject/spec-posture-block", map[string]any{
		"tool":           strings.TrimSpace(attemptedTool),
		"phase_required": phaseReq,
		"phase_name":     phaseName,
		"what":           copy["what"],
		"cause":          copy["cause"],
		"why":            copy["why"],
		"fix":            copy["fix"],
		"instead":        copy["instead"],
		"required":       required,
		"progress":       strings.TrimSpace(progressCompact),
		"code":           rejectCode,
		"details":        details,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(block), nil
}
