package hitl

import (
	"fmt"
	"math"

	"github.com/lycaon/lycaon/pkg/api"
)

// inspectAction projects presentation only. The original action still supplies
// every grant, subject identity and reviewed effect to NewApprovalPlan.
func (i *NativeActionInputs) inspectAction(action ProposedAction) (ProposedAction, error) {
	if i == nil || i.lifetime == nil {
		return ProposedAction{}, fmt.Errorf("native presentation requires reviewed inputs")
	}
	r := i.lifetime
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx == nil || r.projection == nil {
		return ProposedAction{}, fmt.Errorf("native presentation requires live bounded capture producer")
	}
	count := int64(len(action.Invocation.Files)) + int64(len(action.Egress.DeclaredDestinations)) + int64(len(action.Mutations.FileChanges))
	if count > (math.MaxInt64-8192)/1024 {
		return ProposedAction{}, fmt.Errorf("native presentation memory bound overflow")
	}
	if err := r.retainInspection(8192 + 1024*count); err != nil {
		return ProposedAction{}, err
	}
	changes := make([]any, len(action.Mutations.FileChanges))
	for n, c := range action.Mutations.FileChanges {
		changes[n] = map[string]any{"path": c.Path, "from_path": c.FromPath, "before": c.Before, "after": c.After, "preview_note": c.PreviewNote}
	}
	fields := map[string]any{
		"arguments": i.arguments, "tool": action.Invocation.Tool,
		"presentation_tool": action.Presentation.PresentationTool,
		"command":           action.Presentation.Command, "impact": action.Presentation.EstimatedImpact,
		"project_dir": action.Scope.ProjectDir, "files": action.Invocation.Files,
		"destinations": action.Egress.DeclaredDestinations, "file_changes": changes,
	}
	screened, err := r.projectInspection(fields)
	if err != nil {
		return ProposedAction{}, err
	}
	arguments, ok := screened["arguments"].(map[string]any)
	if !ok {
		return ProposedAction{}, fmt.Errorf("native inspection changed arguments shape")
	}
	action.Invocation.Args, err = i.encodeInspection(arguments)
	if err != nil {
		return ProposedAction{}, err
	}
	strings := []struct {
		key string
		dst *string
	}{
		{"tool", &action.Invocation.Tool}, {"presentation_tool", &action.Presentation.PresentationTool},
		{"command", &action.Presentation.Command}, {"impact", &action.Presentation.EstimatedImpact},
		{"project_dir", &action.Scope.ProjectDir},
	}
	for _, field := range strings {
		*field.dst, err = r.inspectionText(screened[field.key])
		if err != nil {
			return ProposedAction{}, err
		}
	}
	action.Invocation.Files, err = r.inspectionStrings(screened["files"])
	if err != nil {
		return ProposedAction{}, err
	}
	action.Egress.DeclaredDestinations, err = r.inspectionStrings(screened["destinations"])
	if err != nil {
		return ProposedAction{}, err
	}
	projectedChanges, ok := screened["file_changes"].([]any)
	if !ok || len(projectedChanges) != len(action.Mutations.FileChanges) {
		return ProposedAction{}, fmt.Errorf("native inspection changed file preview shape")
	}
	previews := append([]api.ApprovalFileChange(nil), action.Mutations.FileChanges...)
	for n, value := range projectedChanges {
		change, ok := value.(map[string]any)
		if !ok {
			return ProposedAction{}, fmt.Errorf("native inspection changed file preview object")
		}
		for _, field := range []struct {
			key string
			dst *string
		}{{"path", &previews[n].Path}, {"from_path", &previews[n].FromPath}, {"before", &previews[n].Before}, {"after", &previews[n].After}, {"preview_note", &previews[n].PreviewNote}} {
			*field.dst, err = r.inspectionText(change[field.key])
			if err != nil {
				return ProposedAction{}, err
			}
		}
	}
	action.Mutations.FileChanges = previews
	return action, nil
}

// These methods run under the review lifetime lock. Output formatting and
// structural copies are admitted in addition to the capture producer's lease.
func (r *NativeActionReview) retainInspection(n int64) error {
	release, err := r.admission.ReserveProgramState(r.ctx, n)
	if err != nil {
		return err
	}
	if release == nil {
		return fmt.Errorf("native inspection admission returned no lease")
	}
	r.releases = append(r.releases, release)
	return nil
}

func (r *NativeActionReview) projectInspection(fields map[string]any) (map[string]any, error) {
	screened, release, err := r.projection.ProjectOperationInputs(r.ctx, r.admission, fields)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, err
	}
	if release == nil || screened == nil {
		if release != nil {
			release()
		}
		return nil, fmt.Errorf("native inspection producer returned no object or lease")
	}
	r.releases = append(r.releases, release)
	return screened, nil
}

func (r *NativeActionReview) inspectionText(value any) (string, error) {
	text, ok := value.(string)
	if !ok || int64(len(text)) > (math.MaxInt64-128)/8 {
		return "", fmt.Errorf("native inspection changed text shape or exceeded its bound")
	}
	if err := r.retainInspection(128 + 8*int64(len(text))); err != nil {
		return "", err
	}
	return reviewDisplayText(text), nil
}

func (r *NativeActionReview) inspectionStrings(value any) ([]string, error) {
	var count int
	switch list := value.(type) {
	case []string:
		count = len(list)
	case []any:
		count = len(list)
	default:
		return nil, fmt.Errorf("native inspection changed text list shape")
	}
	if int64(count) > (math.MaxInt64-128)/32 {
		return nil, fmt.Errorf("native inspection list bound overflow")
	}
	if err := r.retainInspection(128 + 32*int64(count)); err != nil {
		return nil, err
	}
	result := make([]string, count)
	for n := range count {
		var raw any
		switch list := value.(type) {
		case []string:
			raw = list[n]
		case []any:
			raw = list[n]
		}
		text, err := r.inspectionText(raw)
		if err != nil {
			return nil, err
		}
		result[n] = text
	}
	return result, nil
}
