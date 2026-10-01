// Package verification separates the choice of validation from its observed outcome.
package verification

import "strings"

const (
	Inspection = "inspection"
	Targeted   = "targeted"
	Project    = "project"
	Blocked    = "blocked"
)

// Assessment records the selected validation method and rationale.
type Assessment struct {
	Method string `json:"method"`
	Reason string `json:"reason"`
}

func (a *Assessment) Valid() bool {
	if a == nil || strings.TrimSpace(a.Reason) == "" || len(a.Reason) > 2000 {
		return false
	}
	switch a.Method {
	case Inspection, Targeted, Project, Blocked:
		return true
	default:
		return false
	}
}
