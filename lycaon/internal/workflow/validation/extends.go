package validation

import (
	"errors"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

func ExtendsChainErrors(err error) []api.ComposeValidationError {
	msg := err.Error()
	code := workflowdiag.MustCode("extends_error")
	data := map[string]any{"detail": msg, "extends": ""}
	var extendsErr *workflowdef.ExtendsError
	if !errors.As(err, &extendsErr) {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(code, "extends", data)}
	}
	data["extends"] = extendsErr.Ref
	switch extendsErr.Kind {
	case workflowdef.ExtendsErrorDepth:
		code = workflowdiag.MustCode("extends_depth_exceeded")
	case workflowdef.ExtendsErrorUnknown:
		code = workflowdiag.MustCode("unknown_extends_parent")
	case workflowdef.ExtendsErrorCycle:
		code = workflowdiag.MustCode("extends_cycle")
	}
	return []api.ComposeValidationError{workflowdiag.EmitDefault(code, "extends", data)}
}
