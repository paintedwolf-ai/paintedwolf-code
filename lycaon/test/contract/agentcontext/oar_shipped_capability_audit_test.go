package contract

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

type copyGuardFrame struct {
	raw      string
	resolved string
	negated  bool
}

// reviewedBannerResiduals lists the 5 legacy non-OAR HintEntry banners rendered
// through GuidanceRenderer outside oar.GuardPipeline (no CallReach at render time).
var reviewedBannerResiduals = map[string][]string{
	"TOOL_OUTPUT_TRUNCATED":        {"command"},
	"TOOL_OUTPUT_SPILL_CAPPED":     {"command"},
	"OVERLAY_PROMOTE_SPILL":        {"command", "preview_overlay"},
	"WEB_SEARCH_DUPLICATE_QUERY":   {"fetch_url"},
	"WORKFLOW_PHASE_EXIT_REQUIRED": {"workflow_advance"},
}

var commandCapabilityGuards = map[string]bool{
	"paintedwolf.can_command":                 true,
	"paintedwolf.command_surface":             true,
	"paintedwolf.native_operation":            true,
	"paintedwolf.command_form":                true,
	"paintedwolf.profile_mutation_capable":    true,
	"paintedwolf.command_operation_reachable": true,
}

var templateTagRE = regexp.MustCompile(`(?s)(\{#.*?#\}|\{%.*?%\}|\{\{.*?\}\})`)

func auditPolicyDocument(slash, code string, entry guidance.HintEntry, vocab *toolvocab.Catalog, out *[]oarViolation) {
	for _, cond := range append([]string{entry.When}, entry.Flow...) {
		if _, err := parseOARConditionAST(cond); err != nil {
			*out = append(*out, oarViolation{
				Source: slash, Expression: cond, Action: "condition_ast", Reason: err.Error(),
			})
		}
	}
	selected := slices.Clone(entry.Tools)
	selected = append(selected, selectorStrings(entry.Selector, "tool")...)
	selected = append(selected, selectorStrings(entry.Selector, "tools")...)
	selected = append(selected, selectorStrings(entry.Selector, "paintedwolf.operation")...)
	selected = append(selected, entry.Operations()...)
	selected = append(selected, oarcore.ConjunctValues(entry.When, "tool")...)
	for name, val := range map[string]string{
		"title": entry.Title, "what": entry.What, "cause": entry.Cause, "why": entry.Why,
	} {
		auditCopyTemplate(slash+"#"+name, code, val, entry.IsOAR(), false, selected, vocab, out)
	}
	for name, val := range map[string]string{"fix": entry.Fix, "instead": entry.Instead} {
		auditCopyTemplate(slash+"#"+name, code, val, entry.IsOAR(), true, selected, vocab, out)
	}
	auditCopyTemplate(slash+"#message", code, entry.Message, entry.IsOAR(), !entry.IsOAR(), selected, vocab, out)
}

func selectorStrings(sel map[string]any, key string) []string {
	raw, ok := sel[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case string:
		return []string{strings.TrimSpace(v)}
	case []string:
		return slices.Clone(v)
	case []any:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func auditNextStepsList(source, code string, steps []oar.NextStep, vocab *toolvocab.Catalog, out *[]oarViolation) {
	for i, step := range steps {
		loc := fmt.Sprintf("%s#steps[%d]", source, i)
		action := step.Tool
		if len(step.Program) > 0 {
			action = "command:" + step.Program[0]
		}
		if w := strings.TrimSpace(step.When); w != "" {
			ast, err := parseOARConditionAST(w)
			if err != nil {
				*out = append(*out, oarViolation{Source: loc, Expression: w, Action: action, Reason: err.Error()})
			} else {
				auditNextStepWhenAST(loc, w, action, step.Tool, ast, out)
			}
		}
		auditCopyTemplate(loc+".say", code, step.Say, true, false, nil, vocab, out)
		if oth := strings.TrimSpace(step.Otherwise); oth != "" {
			auditOtherwiseText(loc+".otherwise", code, oth, vocab, out)
		}
		auditNextStepsList(loc+".or", code, step.Or, vocab, out)
	}
}

func auditNextStepWhenAST(loc, expr, action, stepTool string, ast *condNode, out *[]oarViolation) {
	idents := map[string]bool{}
	collectCondIdents(ast, idents)
	for id := range idents {
		bad := slices.Contains([]string{
			"paintedwolf.session_mode", "paintedwolf.session_posture",
			"paintedwolf.surface", "paintedwolf.session_role",
			"paintedwolf.has_git", "paintedwolf.ask_worker_writes",
		}, id) || strings.HasPrefix(id, "paintedwolf.profile_")
		if bad {
			*out = append(*out, oarViolation{
				Source: loc, Expression: expr, Action: action,
				Reason: "next-step predicate reconstructs capability from session/profile state " + id + "; derive a host fact instead",
			})
		}
		if stepTool != "" && id == "paintedwolf.can_"+stepTool {
			*out = append(*out, oarViolation{
				Source: loc, Expression: expr, Action: action,
				Reason: "next-step predicate repeats " + id + " already enforced by step.tool reach",
			})
		}
	}
	if idents["paintedwolf.command_operation_reachable"] && idents["paintedwolf.admitted_command_forms"] {
		*out = append(*out, oarViolation{
			Source: loc, Expression: expr, Action: action,
			Reason: "next-step predicate repeats size(admitted_command_forms) already guaranteed by command_operation_reachable",
		})
	}
}

func auditOtherwiseText(loc, code, oth string, vocab *toolvocab.Catalog, out *[]oarViolation) {
	auditCopyTemplate(loc, code, oth, true, false, nil, vocab, out)
	if loaderPromiseRE.MatchString(oth) {
		*out = append(*out, oarViolation{
			Source: loc, Expression: oth, Action: "request_tools",
			Reason: "next-step otherwise branch promises tool loading after host reach already ruled out loader recovery",
		})
	}
	needsCmd, tools := promisedActionsInText(vocab, oth)
	if needsCmd {
		*out = append(*out, oarViolation{
			Source: loc, Expression: oth, Action: "command",
			Reason: "next-step otherwise branch promises command when step is unreachable",
		})
	}
	for _, tool := range tools {
		*out = append(*out, oarViolation{
			Source: loc, Expression: oth, Action: tool,
			Reason: "next-step otherwise branch promises tool call when step is unreachable",
		})
	}
}

func auditCopyTemplate(
	source, code, tpl string,
	isOAR, checkActions bool,
	selectedTools []string,
	vocab *toolvocab.Catalog,
	out *[]oarViolation,
) {
	if strings.TrimSpace(tpl) == "" {
		return
	}
	aliases := map[string]string{}
	var stack []copyGuardFrame
	last := 0
	for _, idx := range templateTagRE.FindAllStringIndex(tpl, -1) {
		if checkActions && idx[0] > last {
			checkSegmentActions(source, code, tpl[last:idx[0]], isOAR, stack, selectedTools, vocab, out)
		}
		tok := tpl[idx[0]:idx[1]]
		last = idx[1]
		if strings.HasPrefix(tok, "{{") && strings.Contains(tok, "|") {
			*out = append(*out, oarViolation{
				Source: source, Expression: tok, Action: "filter",
				Reason: "embedded copy uses template filter pipe; keep copy projection in host facts",
			})
		}
		if !strings.HasPrefix(tok, "{%") {
			continue
		}
		body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(tok, "{%"), "%}"))
		body = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(body, "-"), "-"))
		kw, arg, _ := strings.Cut(body, " ")
		arg = strings.TrimSpace(arg)
		switch kw {
		case "set":
			lhs, rhs, ok := strings.Cut(arg, "=")
			if ok {
				aliases[strings.TrimSpace(lhs)] = strings.TrimSpace(rhs)
			}
			*out = append(*out, oarViolation{
				Source: source, Expression: body, Action: strings.TrimSpace(lhs),
				Reason: "local template assignment {% set %} is forbidden; derive capability facts on the host",
			})
		case "with", "include":
			*out = append(*out, oarViolation{
				Source: source, Expression: body, Action: kw,
				Reason: "embedded template tag {%" + kw + "%} is forbidden in OAR/policy copy",
			})
		case "if", "elif":
			if strings.Contains(arg, "|") {
				*out = append(*out, oarViolation{
					Source: source, Expression: body, Action: "filter",
					Reason: "condition uses filter pipe inside template branch",
				})
			}
			resolved := resolveAliasExpr(arg, aliases)
			if _, err := parseOARConditionAST(resolved); err != nil {
				*out = append(*out, oarViolation{
					Source: source, Expression: body, Action: "condition_ast", Reason: err.Error(),
				})
			}
			frame := copyGuardFrame{raw: arg, resolved: resolved}
			if kw == "if" {
				stack = append(stack, frame)
			} else if len(stack) > 0 {
				stack[len(stack)-1] = frame
			}
		case "else":
			if len(stack) > 0 {
				stack[len(stack)-1].negated = true
			}
		case "endif":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			*out = append(*out, oarViolation{
				Source: source, Expression: body, Action: kw,
				Reason: "unhandled template control tag in OAR/policy copy",
			})
		}
	}
	if checkActions && last < len(tpl) {
		checkSegmentActions(source, code, tpl[last:], isOAR, stack, selectedTools, vocab, out)
	}
}

func resolveAliasExpr(expr string, aliases map[string]string) string {
	trimmed := strings.TrimSpace(expr)
	for name, rhs := range aliases {
		if trimmed == name {
			return rhs
		}
		if trimmed == "not "+name || trimmed == "!"+name {
			return "!(" + rhs + ")"
		}
	}
	return trimmed
}

func checkSegmentActions(
	source, code, text string,
	isOAR bool,
	stack []copyGuardFrame,
	selectedTools []string,
	vocab *toolvocab.Catalog,
	out *[]oarViolation,
) {
	needsCmd, tools := promisedActionsInText(vocab, text)
	if !needsCmd && len(tools) == 0 {
		return
	}
	posFacts, exprSummary := positiveGuardFacts(stack)
	residualExempt := func(action string) bool {
		return !isOAR && !hasForbiddenBannerGuard(stack) && slices.Contains(reviewedBannerResiduals[code], action)
	}
	if needsCmd && !slices.Contains(selectedTools, "command") && !hasAnyGuard(posFacts, commandCapabilityGuards) && !residualExempt("command") {
		*out = append(*out, oarViolation{
			Source: source, Expression: exprSummary, Action: "command",
			Reason: "remedy copy promises command without a host command-reach guard or next-steps check",
		})
	}
	for _, tool := range tools {
		guarded := slices.Contains(selectedTools, tool) ||
			posFacts["paintedwolf.can_"+tool] ||
			posFacts["paintedwolf.receiving_tool_"+tool] ||
			posFacts["paintedwolf.text_action_callable_tools"] ||
			residualExempt(tool)
		if !guarded {
			*out = append(*out, oarViolation{
				Source: source, Expression: exprSummary, Action: tool,
				Reason: "remedy copy promises tool " + tool + " without host reach guard paintedwolf.can_" + tool + " or next-steps",
			})
		}
	}
}

func positiveGuardFacts(stack []copyGuardFrame) (map[string]bool, string) {
	facts := map[string]bool{}
	var parts []string
	for _, f := range stack {
		label := f.raw
		if f.resolved != f.raw {
			label = f.raw + " => " + f.resolved
		}
		if f.negated {
			label = "else(" + label + ")"
		}
		parts = append(parts, label)
		e := strings.TrimSpace(f.resolved)
		isNot := strings.HasPrefix(e, "not ") || strings.HasPrefix(e, "!")
		if isNot {
			e = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(e, "not "), "!"))
		}
		if !(f.negated != isNot) {
			facts[e] = true
		}
	}
	if len(parts) == 0 {
		return facts, "<unguarded>"
	}
	return facts, strings.Join(parts, " && ")
}

func hasForbiddenBannerGuard(stack []copyGuardFrame) bool {
	for _, f := range stack {
		ast, err := parseOARConditionAST(f.resolved)
		if err != nil {
			return true
		}
		idents := map[string]bool{}
		collectCondIdents(ast, idents)
		for id := range idents {
			if slices.Contains([]string{
				"paintedwolf.session_mode", "paintedwolf.session_posture",
				"paintedwolf.surface", "paintedwolf.session_role",
				"paintedwolf.has_git", "paintedwolf.ask_worker_writes",
			}, id) || strings.HasPrefix(id, "paintedwolf.profile_") {
				return true
			}
		}
	}
	return false
}

func hasAnyGuard(posFacts, allowed map[string]bool) bool {
	for k := range posFacts {
		if allowed[k] {
			return true
		}
	}
	return false
}
