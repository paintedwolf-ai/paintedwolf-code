// Package verdictcall composes the submit_verdict call a review phase accepts
// from the catalog schema's verdict member fragments.
package verdictcall

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/tools/argdiag"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// verdictFragmentsKey holds the catalog's verdict member fragments in the
// submit_verdict schema, one per verdict_schema type word.
const verdictFragmentsKey = "$defs"

// Fragments for the decision member and for any type word the catalog does
// not name, which verdict_schema treats as free text.
const (
	verdictFragmentDecision = "decision"
	verdictFragmentText     = "text"
)

// Compose builds the submit_verdict arguments a review phase
// accepts: the catalog call with `verdict` narrowed to the phase's declared
// members. The catalog fragments carry the copy; the phase supplies the
// decision values, claim statuses, rating questions, and follow-up policy.
// Citations nested in claims and assessments are described by reference to
// the call-level entry rather than restated; the verdict owner validates them.
func Compose(base map[string]any, rl workflowdef.ReviewLoopDef, brief *workflowdef.Brief) (map[string]any, error) {
	fragments, _ := base[verdictFragmentsKey].(map[string]any)
	if len(fragments) == 0 {
		return nil, fmt.Errorf("submit_verdict schema declares no verdict member fragments")
	}
	out := jsonvalue.CloneMap(base)
	delete(out, verdictFragmentsKey)
	props, _ := out["properties"].(map[string]any)
	if props == nil {
		return nil, fmt.Errorf("submit_verdict schema declares no properties")
	}
	fields := make([]string, 0, len(rl.VerdictSchema))
	for field := range rl.VerdictSchema {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	members := make(map[string]any, len(fields))
	required := make([]any, 0, len(fields))
	for _, field := range fields {
		kind := strings.TrimSpace(rl.VerdictSchema[field])
		name := verdictFragmentName(field, kind)
		fragment, ok := fragments[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("submit_verdict schema has no %q fragment for verdict member %q", name, field)
		}
		member := jsonvalue.CloneMap(fragment)
		switch name {
		case verdictFragmentDecision:
			member["enum"] = anySlice(rl.Decisions())
		case workflowdef.VerdictClaimsType:
			fillClaimsSchema(member, rl, brief)
		}
		members[field] = member
		required = append(required, field)
	}
	props["verdict"] = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             required,
		"properties":           members,
	}
	return out, nil
}

// CheckFragments refuses a catalog call schema that cannot compose
// every verdict_schema type word.
func CheckFragments(catalog map[string]any) error {
	fragments, _ := catalog[verdictFragmentsKey].(map[string]any)
	for _, name := range []string{verdictFragmentDecision, verdictFragmentText, workflowdef.VerdictClaimsType, workflowdef.VerdictSetAsidesType, workflowdef.VerdictCoverageType} {
		if _, ok := fragments[name].(map[string]any); !ok {
			return fmt.Errorf("submit_verdict catalog schema has no %q verdict member fragment", name)
		}
	}
	return nil
}

// Outline renders the verdict member of a composed call schema.
func Outline(call map[string]any) string {
	props, _ := call["properties"].(map[string]any)
	verdict, _ := props["verdict"].(map[string]any)
	return argdiag.Outline(verdict)
}

// Attach gives a phase-exit view the call its phase accepts and that call's
// verdict outline.
func Attach(view *inject.PhaseExitView, catalog map[string]any, rl workflowdef.ReviewLoopDef, brief *workflowdef.Brief) error {
	call, err := Compose(catalog, rl, brief)
	if err != nil {
		return err
	}
	view.SubmitVerdictArgsSchema = call
	view.VerdictOutline = Outline(call)
	return nil
}

func verdictFragmentName(field, kind string) string {
	switch {
	case field == workflowdef.VerdictDecisionKey:
		return verdictFragmentDecision
	case kind == workflowdef.VerdictClaimsType, kind == workflowdef.VerdictSetAsidesType, kind == workflowdef.VerdictCoverageType:
		return kind
	default:
		return verdictFragmentText
	}
}

func fillClaimsSchema(member map[string]any, rl workflowdef.ReviewLoopDef, brief *workflowdef.Brief) {
	item, _ := member["items"].(map[string]any)
	props, _ := item["properties"].(map[string]any)
	if props == nil {
		return
	}
	if words := rl.StatusWords(); len(words) > 0 {
		status, _ := props["status"].(map[string]any)
		if status != nil {
			status["enum"] = anySlice(words)
		}
		// One declared word is the only valid value, so the host supplies it.
		if len(words) > 1 {
			required, _ := item["required"].([]any)
			item["required"] = append(required, "status")
		}
	}
	if answers, ok := props["answers"].(map[string]any); ok {
		if brief == nil || len(brief.Dimensions) == 0 {
			delete(props, "answers")
		} else {
			fillAnswersSchema(answers, brief)
		}
	}
	if rl.FollowupAttempts == 0 {
		delete(props, "question")
	}
}

func fillAnswersSchema(answers map[string]any, brief *workflowdef.Brief) {
	dims := make(map[string]any, len(brief.Dimensions))
	required := make([]any, 0, len(brief.Dimensions))
	for _, d := range brief.Dimensions {
		values := make([]any, 0, len(d.Values)+1)
		for _, v := range d.Values {
			values = append(values, v.ID)
		}
		if d.AllowUnknown {
			values = append(values, workflowdef.BriefUnknown)
		}
		dim := map[string]any{"type": "string", "enum": values}
		if q := strings.TrimSpace(d.Question); q != "" {
			dim["description"] = q
		}
		dims[d.ID] = dim
		required = append(required, d.ID)
	}
	answers["properties"] = dims
	answers["required"] = required
}

func anySlice(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
