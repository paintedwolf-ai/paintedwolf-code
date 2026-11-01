package policyindex

import (
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/agentpolicy"
)

type scopeLimitError struct {
	Count int `json:"scope_count"`
	Limit int `json:"scope_limit"`
}

func (e *scopeLimitError) Error() string {
	return fmt.Sprintf("policy operation spans %d jurisdictions; limit is %d", e.Count, e.Limit)
}

func policyFailures(err error) []*agentpolicy.ScopeError {
	if err == nil {
		return nil
	}
	if _, limited := err.(*scopeLimitError); limited {
		return nil
	}
	if group, ok := err.(interface{ Unwrap() []error }); ok {
		var failures []*agentpolicy.ScopeError
		for _, item := range group.Unwrap() {
			failures = append(failures, policyFailures(item)...)
		}
		return failures
	}
	var scoped *agentpolicy.ScopeError
	if errors.As(err, &scoped) {
		return []*agentpolicy.ScopeError{scoped}
	}
	return []*agentpolicy.ScopeError{{Cause: err}}
}

func failedMutation(err error, mutations []agentpolicy.Scope) bool {
	for _, failure := range policyFailures(err) {
		if failure.Scope.RootID == "" {
			return len(mutations) > 0
		}
		for _, scope := range mutations {
			if scope == failure.Scope {
				return true
			}
		}
	}
	return false
}
