package conditions

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrForbiddenCondition is returned when Register or Evaluate is called with a banned vocabulary id.
var ErrForbiddenCondition = errors.New("forbidden condition")

// forbiddenConditionIDs are exact ids that must never appear in config or Register().
var forbiddenConditionIDs = map[string]struct{}{
	"parallel_review_test_complete": {},
	"request_user_input":            {},
	"verify_evidence_passed":        {},
	"verify_evidence_missing":       {},
	"test_evidence_passed":          {},
	"security_gate_passed":          {},
	"security_gate_failed":          {},
	"security_gate_missing":         {},
	"mode_is":                       {},
	"mode_unresolved":               {},
}

var forbiddenNamePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^den_[a-z0-9_]+$`),
	regexp.MustCompile(`^rally_[a-z0-9_]+$`),
	regexp.MustCompile(`^call_[a-z][a-z0-9_]*$`),
	regexp.MustCompile(`^stage_[a-z0-9_]+_complete$`),
	regexp.MustCompile(`^parallel_review_test_complete$`),
	regexp.MustCompile(`^user_input_[a-z0-9_]+$`),
	regexp.MustCompile(`^request_user_input$`),
	regexp.MustCompile(`^verify_evidence_(passed|missing)$`),
	regexp.MustCompile(`^test_evidence_passed$`),
	regexp.MustCompile(`^security_gate_(passed|failed|missing)$`),
	regexp.MustCompile(`^plan_critic(_alt)?$`),
	regexp.MustCompile(`^mode_is$`),
	regexp.MustCompile(`^mode_unresolved$`),
}

var forbiddenReplacements = map[string]string{
	"stage_research_complete":       "topology_stage_complete with bind_topology_stage: research",
	"stage_plan_complete":           "topology_stage_complete with bind_topology_stage: plan",
	"stage_implement_complete":      "topology_stage_complete with bind_topology_stage: implement",
	"stage_review_complete":         "topology_stage_complete with bind_topology_stage: review",
	"stage_test_complete":           "topology_stage_complete with bind_topology_stage: test",
	"parallel_review_test_complete": "parallel_stages_complete with bind_parallel_group: [review, test]",
	"verify_evidence_passed":        "evidence_passed:verify",
	"verify_evidence_missing":       "evidence_missing:verify",
	"test_evidence_passed":          "evidence_passed:test",
	"security_gate_passed":          "evidence_passed:security",
	"security_gate_failed":          "evidence_missing:security",
	"security_gate_missing":         "evidence_missing:security",
	"request_user_input":            "user_feedback_* or user_decision_*",
	"mode_is":                       "posture_is:<posture>",
	"mode_unresolved":               "posture_unresolved",
}

// IsForbidden reports whether name must never be registered (see docs/workflows.md).
func IsForbidden(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if _, ok := forbiddenConditionIDs[name]; ok {
		return true
	}
	for _, re := range forbiddenNamePatterns {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

// ReplacementHint returns the replacement for a forbidden id, if known.
func ReplacementHint(name string) string {
	name = strings.TrimSpace(name)
	if hint, ok := forbiddenReplacements[name]; ok {
		return hint
	}
	if strings.HasPrefix(name, "stage_") && strings.HasSuffix(name, "_complete") {
		stage := strings.TrimSuffix(strings.TrimPrefix(name, "stage_"), "_complete")
		return fmt.Sprintf("topology_stage_complete with bind_topology_stage: %s", stage)
	}
	if strings.HasPrefix(name, "user_input_") {
		return "user_feedback_* (open text) or user_decision_* (structured choice)"
	}
	return "see docs/workflows.md"
}

// ForbiddenConfigError formats a boot/load validation error with replacement guidance.
func ForbiddenConfigError(name string) error {
	name = strings.TrimSpace(name)
	return fmt.Errorf("config vocabulary: forbidden id %q — use %s", name, ReplacementHint(name))
}

// ForbiddenPredicateRoot returns the predicate id before an optional :suffix.
func ForbiddenPredicateRoot(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.Index(id, ":"); i >= 0 {
		return id[:i]
	}
	return id
}

// ForbiddenPredicateConfigError validates a predicate ID for composition and loading.
func ForbiddenPredicateConfigError(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	root := ForbiddenPredicateRoot(id)
	if !IsForbidden(root) {
		return nil
	}
	if root == "mode_is" && strings.Contains(id, ":") {
		suffix := strings.TrimPrefix(id, root+":")
		return fmt.Errorf("config vocabulary: forbidden id %q — use posture_is:%s", id, suffix)
	}
	return ForbiddenConfigError(root)
}

// ForbiddenPredicateReplacementHint returns a replacement hint for compose 422 responses.
func ForbiddenPredicateReplacementHint(id string) string {
	id = strings.TrimSpace(id)
	root := ForbiddenPredicateRoot(id)
	if !IsForbidden(root) {
		return ""
	}
	if root == "mode_is" && strings.Contains(id, ":") {
		return "posture_is:" + strings.TrimPrefix(id, root+":")
	}
	return ReplacementHint(root)
}

// IsCatalogStub reports catalog-only scan predicates.
func IsCatalogStub(name string) bool {
	name = strings.TrimSpace(name)
	for _, id := range scanCatalogStubIDs {
		if id == name {
			return true
		}
	}
	return false
}

func checkForbiddenRegister(name string) error {
	if IsForbidden(name) {
		return fmt.Errorf("%w: %s", ErrForbiddenCondition, name)
	}
	return nil
}
