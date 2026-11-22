package contract

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

type copyGuardFrame struct {
	raw      string
	resolved string
	negated  bool
}

var templateTagRE = regexp.MustCompile(`(?s)(\{#.*?#\}|\{%.*?%\}|\{\{.*?\}\})`)

func isForbiddenCapabilityIdent(id string) bool {
	return slices.Contains([]string{
		"paintedwolf.session_mode", "paintedwolf.session_posture",
		"paintedwolf.surface", "paintedwolf.session_role",
		"paintedwolf.has_git", "paintedwolf.ask_worker_writes",
	}, id) || strings.HasPrefix(id, "paintedwolf.profile_")
}

func auditPolicyDocument(slash string, entry guidance.HintEntry, vocab *toolvocab.Catalog, out *[]oarViolation) {
	for _, cond := range append([]string{entry.When}, entry.Flow...) {
		if _, err := parseOARConditionIdents(cond); err != nil {
			*out = append(*out, oarViolation{
				Source: slash, Expression: cond, Action: "condition_ast", Reason: err.Error(),
			})
		}
	}
	fields := []struct {
		name string
		val  string
	}{
		{"title", entry.Title},
		{"message", entry.Message},
		{"what", entry.What},
		{"cause", entry.Cause},
		{"why", entry.Why},
		{"fix", entry.Fix},
		{"instead", entry.Instead},
	}
	for _, f := range fields {
		auditCopyTemplate(slash+"#"+f.name, f.val, vocab, out)
	}
}

func auditNextStepsList(source string, steps []oar.NextStep, vocab *toolvocab.Catalog, out *[]oarViolation) {
	for i, step := range steps {
		loc := fmt.Sprintf("%s#steps[%d]", source, i)
		action := step.Tool
		if len(step.Program) > 0 {
			action = "command:" + step.Program[0]
		}
		whenIdents := map[string]bool{}
		if w := strings.TrimSpace(step.When); w != "" {
			parsedIdents, err := parseOARConditionIdents(w)
			if err != nil {
				*out = append(*out, oarViolation{Source: loc, Expression: w, Action: action, Reason: err.Error()})
			} else {
				whenIdents = parsedIdents
				auditNextStepWhenAST(loc, w, action, step.Tool, whenIdents, out)
			}
		}
		if step.Tool == "command" && len(step.Program) == 0 {
			auditCommandStepSchemaClaims(loc+".say", step.When, step.Say, whenIdents, out)
		}
		auditCopyTemplate(loc+".say", step.Say, vocab, out)
		if oth := strings.TrimSpace(step.Otherwise); oth != "" {
			auditOtherwiseText(loc+".otherwise", oth, vocab, out)
		}
		auditNextStepsList(loc+".or", step.Or, vocab, out)
	}
}

func auditCommandStepSchemaClaims(loc, whenExpr, say string, whenIdents map[string]bool, out *[]oarViolation) {
	m := prunedCmdFieldsRE.FindStringSubmatch(say)
	if len(m) == 0 {
		return
	}
	if whenIdents["paintedwolf.admitted_process_capability_fields"] || whenIdents["paintedwolf.command_external_reachable"] {
		return
	}
	expr := strings.TrimSpace(whenExpr)
	if expr == "" {
		expr = "<unguarded>"
	}
	*out = append(*out, oarViolation{
		Source: loc, Expression: expr, Action: m[1],
		Reason: "command next-step recommends schema-pruned field " + m[1] + " without projected-schema or external-reach host fact",
	})
}

func auditNextStepWhenAST(loc, expr, action, stepTool string, idents map[string]bool, out *[]oarViolation) {
	for id := range idents {
		if isForbiddenCapabilityIdent(id) {
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

func auditOtherwiseText(loc, oth string, vocab *toolvocab.Catalog, out *[]oarViolation) {
	auditCopyTemplate(loc, oth, vocab, out)
	if loaderPromiseRE.MatchString(oth) {
		*out = append(*out, oarViolation{
			Source: loc, Expression: oth, Action: "request_tools",
			Reason: "next-step otherwise branch promises tool loading after host reach already ruled out loader recovery",
		})
	}
	needsCmd, progs, tools := promisedActionsInText(vocab, oth)
	if needsCmd || len(progs) > 0 {
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

func auditCopyTemplate(source, tpl string, vocab *toolvocab.Catalog, out *[]oarViolation) {
	if strings.TrimSpace(tpl) == "" {
		return
	}
	aliases := map[string]string{}
	var stack []copyGuardFrame
	last := 0
	for _, idx := range templateTagRE.FindAllStringIndex(tpl, -1) {
		if idx[0] > last {
			checkSegmentActions(source, tpl[last:idx[0]], stack, vocab, out)
		}
		tok := tpl[idx[0]:idx[1]]
		last = idx[1]
		if !strings.HasPrefix(tok, "{%") {
			continue
		}
		body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(tok, "{%"), "%}"))
		body = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(body, "-"), "-"))
		kw, arg, _ := strings.Cut(body, " ")
		arg = strings.TrimSpace(arg)
		applyTemplateControlTag(source, body, kw, arg, aliases, &stack, out)
	}
	if last < len(tpl) {
		checkSegmentActions(source, tpl[last:], stack, vocab, out)
	}
}

func applyTemplateControlTag(
	source, body, kw, arg string,
	aliases map[string]string,
	stack *[]copyGuardFrame,
	out *[]oarViolation,
) {
	switch kw {
	case "set":
		lhs, rhs, ok := strings.Cut(arg, "=")
		lhs, rhs = strings.TrimSpace(lhs), strings.TrimSpace(rhs)
		if ok && lhs != "" {
			aliases[lhs] = rhs
			if idents, err := parseCopyTemplateExprIdents(rhs); err == nil && len(idents) > 0 {
				for id := range idents {
					if isForbiddenCapabilityIdent(id) {
						*out = append(*out, oarViolation{
							Source: source, Expression: body, Action: lhs,
							Reason: "local template assignment derives capability from " + id + "; compute a host fact instead",
						})
					}
				}
			}
		}
	case "with", "include":
		*out = append(*out, oarViolation{
			Source: source, Expression: body, Action: kw,
			Reason: "embedded template tag {%" + kw + "%} is forbidden in OAR/policy copy",
		})
	case "if", "elif":
		condExpr, _, hasPipe := strings.Cut(arg, "|")
		resolved := resolveAliasExpr(strings.TrimSpace(condExpr), aliases)
		if _, err := parseCopyTemplateExprIdents(resolved); err != nil {
			*out = append(*out, oarViolation{
				Source: source, Expression: body, Action: "condition_ast", Reason: err.Error(),
			})
		}
		if hasPipe {
			resolved = "filter(" + resolved + ")"
		}
		frame := copyGuardFrame{raw: arg, resolved: resolved}
		if kw == "if" {
			*stack = append(*stack, frame)
		} else if len(*stack) > 0 {
			(*stack)[len(*stack)-1] = frame
		}
	case "else":
		if len(*stack) > 0 {
			(*stack)[len(*stack)-1].negated = true
		}
	case "endif":
		if len(*stack) > 0 {
			*stack = (*stack)[:len(*stack)-1]
		}
	default:
		*out = append(*out, oarViolation{
			Source: source, Expression: body, Action: kw,
			Reason: "unhandled template control tag in OAR/policy copy",
		})
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
	source, text string,
	stack []copyGuardFrame,
	vocab *toolvocab.Catalog,
	out *[]oarViolation,
) {
	needsCmd, progs, tools := promisedActionsInText(vocab, text)
	if !needsCmd && len(progs) == 0 && len(tools) == 0 {
		return
	}
	posFacts, exprSummary := positiveGuardFacts(stack)
	for _, prog := range progs {
		if !hasAnyGuard(posFacts, programCapabilityGuards[prog]) {
			*out = append(*out, oarViolation{
				Source: source, Expression: exprSummary, Action: prog,
				Reason: "copy recommends command program " + prog + " without a per-form host reach guard",
			})
		}
	}
	if needsCmd && !hasAnyGuard(posFacts, genericCommandGuards) {
		*out = append(*out, oarViolation{
			Source: source, Expression: exprSummary, Action: "command",
			Reason: "copy recommends command without a current-request host command-reach guard",
		})
	}
	for _, tool := range tools {
		if !posFacts["paintedwolf.can_"+tool] && !posFacts["paintedwolf.receiving_tool_"+tool] {
			*out = append(*out, oarViolation{
				Source: source, Expression: exprSummary, Action: tool,
				Reason: "copy recommends tool " + tool + " without current-request host reach guard paintedwolf.can_" + tool,
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

func hasAnyGuard(posFacts, allowed map[string]bool) bool {
	for k := range posFacts {
		if allowed[k] {
			return true
		}
	}
	return false
}
