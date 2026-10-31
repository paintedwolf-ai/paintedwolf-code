package hitl

import (
	"fmt"
	"math"

	"github.com/lycaon/lycaon/internal/observability"
)

// checkpointText is display data; none of it participates in grant matching.
type checkpointText struct {
	title       string
	description string
	explanation ApprovalExplanation
	reason      string
	subjects    []string
}

func checkpointPresentationText(req CheckpointRequest) (checkpointText, error) {
	var result checkpointText
	if req.ProposedAction != nil && req.ProposedAction.Invocation.NativeInputs != nil {
		return req.ProposedAction.Invocation.NativeInputs.inspectCheckpointText(req)
	}
	result.title = observability.RedactCaptureText(req.Title)
	result.description = observability.RedactCaptureText(req.Description)
	if req.Explanation != nil {
		result.explanation = ApprovalExplanation{
			What: observability.RedactCaptureText(req.Explanation.What), Who: observability.RedactCaptureText(req.Explanation.Who),
			IfWrong: observability.RedactCaptureText(req.Explanation.IfWrong), AllowLine: observability.RedactCaptureText(req.Explanation.AllowLine),
		}
	}
	if req.Repeat != nil {
		result.reason = observability.RedactCaptureText(req.Repeat.ReasonKey)
		result.subjects = append([]string(nil), req.Repeat.Subjects...)
		for n := range result.subjects {
			result.subjects[n] = observability.RedactCaptureText(result.subjects[n])
		}
	}
	return result, nil
}

func (i *NativeActionInputs) inspectCheckpointText(req CheckpointRequest) (checkpointText, error) {
	var result checkpointText
	if i == nil || i.lifetime == nil {
		return result, fmt.Errorf("native checkpoint requires reviewed inputs")
	}
	r := i.lifetime
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx == nil || r.projection == nil {
		return result, fmt.Errorf("native checkpoint requires live bounded capture producer")
	}
	count := 0
	if req.Repeat != nil {
		count = len(req.Repeat.Subjects)
	}
	if int64(count) > (math.MaxInt64-8192)/128 {
		return result, fmt.Errorf("native checkpoint text bound overflow")
	}
	if err := r.retainInspection(8192 + 128*int64(count)); err != nil {
		return result, err
	}
	fields := map[string]any{"title": req.Title, "description": req.Description, "what": "", "who": "", "if_wrong": "", "allow_line": "", "reason": "", "subjects": []string{}}
	if req.Explanation != nil {
		fields["what"], fields["who"] = req.Explanation.What, req.Explanation.Who
		fields["if_wrong"], fields["allow_line"] = req.Explanation.IfWrong, req.Explanation.AllowLine
	}
	if req.Repeat != nil {
		fields["reason"], fields["subjects"] = req.Repeat.ReasonKey, req.Repeat.Subjects
	}
	screened, err := r.projectInspection(fields)
	if err != nil {
		return result, err
	}
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"title", &result.title}, {"description", &result.description},
		{"what", &result.explanation.What}, {"who", &result.explanation.Who},
		{"if_wrong", &result.explanation.IfWrong}, {"allow_line", &result.explanation.AllowLine},
		{"reason", &result.reason},
	} {
		*field.dst, err = r.inspectionText(screened[field.key])
		if err != nil {
			return checkpointText{}, err
		}
	}
	result.subjects, err = r.inspectionStrings(screened["subjects"])
	return result, err
}
