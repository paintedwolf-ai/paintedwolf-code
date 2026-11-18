package definition

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/vocabulary"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

type PredicateErrors []api.ComposeValidationError

func (e PredicateErrors) Error() string { return workflowdiag.Summarize(e) }

func (m Manifest) checkPredicates(overlay bool) error {
	var out PredicateErrors
	for _, phase := range m.PhaseDefs {
		out = append(out, phase.PredicateDiagnostics(fmt.Sprintf("phases[%s]", phase.ID), vocabulary.IsPredicate, overlay)...)
	}
	if len(out) > 0 {
		return out
	}
	return nil
}

func (phase PhaseDef) PredicateDiagnostics(path string, allowed func(string) bool, overlay bool) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	for _, field := range phase.PredicateFields() {
		name := path + "." + field.Path
		if field.Err != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("boolexpr_parse"), name, map[string]any{"detail": field.Err.Error()}))
			continue
		}
		for _, leaf := range field.Leaves {
			out = append(out, workflowdiag.PredicateLeaf(name, leaf, allowed, overlay, field.Path == "human_approval.readiness")...)
		}
	}
	return out
}

func predicateFileError(path string, err error) error {
	var issues PredicateErrors
	if errors.As(err, &issues) {
		for i := range issues {
			issues[i].Field = path + ": " + issues[i].Field
		}
	}
	return fmt.Errorf("%s: %w", path, err)
}
