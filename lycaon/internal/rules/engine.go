// Package rules defines the extensible scaffold rule engine.
//
// The toolpolicy engine evaluates rules before each tool call.
package rules

import (
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// NewEvalContext builds rule input with workflow phase when available.
func NewEvalContext(sess *api.Session, toolName string, args map[string]any, phase, blueprintPath, planContent string) EvalContext {
	mode := api.SessionPosture("")
	projectDir := ""
	projectID := ""
	if sess != nil {
		mode = sess.Posture
		projectDir = sess.WorkspacePath
		// ProjectID lets the trust gate resolve the project row.
		projectID = sess.ProjectID
	}
	return EvalContext{EvalContext: conditions.EvalContext{
		SessionPosture: mode,
		Phase:          phase,
		ToolName:       toolName,
		ToolArgs:       args,
		ProjectDir:     projectDir,
		ProjectID:      projectID,
		BlueprintPath:  blueprintPath,
		PlanContent:    planContent,
	}}
}

// EvalContext is input for rule evaluation before tool execution. It embeds the
// shared condition-registry context so a rule's when: leaves see exactly the
// facts the registry sees.
//
// Only rule-engine-local facts live here. PlanProgress stays here because
// guidance imports conditions.
type EvalContext struct {
	conditions.EvalContext

	OverlayRootPaths []string
	PlanProgress     guidance.PlanProgress
	PostureRules     []string
	ManifestRules    []string
}

// RuleOutcome is the result of evaluating ordered rules.
type RuleOutcome struct {
	Allowed           bool
	Code              string
	Message           string
	RejectCode        string
	PhaseRequired     string
	PhaseRequiredName string
	MinRequired       string
	MaxPlaybook       string
}
