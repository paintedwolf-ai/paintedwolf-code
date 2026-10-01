package tools

import (
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
)

// ExplainGate applies the primary gate's approval copy.
func ExplainGate(base ApprovalExplanation, decision *gate.Decision) ApprovalExplanation {
	if decision == nil {
		return base
	}
	switch decision.Primary {
	case api.GateSecretOutbound:
		base.What = withDetail(base.What, "a credential was found in content about to leave this machine")
		base.IfWrong = "The value would reach a system outside this machine and would have to be rotated."
	case api.GateConsentDrift:
		base.What = withDetail(base.What, "this tool's definition changed since you approved it")
		base.IfWrong = "The tool may now do something other than what you reviewed."
	case api.GateRemotePackageExecution, api.GateRemotePackageExecutionKnown:
		base.What, base.IfWrong = packageExecutionCopy(citedCount(decision, "package.coordinate"))
		base.AllowLine = "this exact package action"
	case api.GateUnobservedChannel:
		base.What = withDetail(base.What, "this runs on a channel the app cannot observe")
		base.IfWrong = "Effects on the far side of this channel are not recorded and cannot be shown to you afterwards."
	case api.GateAuthorityMisuse:
		base.What = withDetail(base.What, citedValue(decision, "detection.rule"))
		external := citedValue(decision, "effect.location") == "external_system"
		unrecoverable := citedValue(decision, "effect.recovery") == "unrecoverable"
		switch {
		case external && unrecoverable:
			base.IfWrong = "This changes an external system and cannot be undone from here."
		case external:
			base.IfWrong = "This changes an external system."
		case unrecoverable:
			base.IfWrong = "This changes something on this machine and cannot be undone by the app."
		default:
			base.IfWrong = "This changes something on this machine."
		}
	case api.GateAgentPolicyChange:
		base.What = "Change project instructions, skills, prompts, or settings that agents follow."
		base.IfWrong = "Later work would follow the changed files."
		base.AllowLine = "this action"
	case api.GateSensitiveLocation:
		base.What = withDetail(base.What, firstNonEmptyCopy(
			citedValue(decision, "location.catalog"),
			"this is a location outside the folders you attached",
		))
		base.What = withBatchNote(base.What, decision)
		base.IfWrong = "This is outside the folders you attached, and it is somewhere worth knowing about."
	case api.GateOutsideRootsWrite:
		base.What = withDetail(base.What, "this writes outside the folders you attached")
		base.What = withBatchNote(base.What, decision)
		base.IfWrong = "Work reaches a part of the machine you did not put in scope."
	case api.GateOutsideRootsRead:
		path := citedValue(decision, "file.path")
		if path != "" {
			base.What = "Read files outside attached folders at " + path + "."
		} else {
			base.What = "Read files outside attached folders."
		}
		base.What = withBatchNote(base.What, decision)
		base.AllowLine = "reading files outside attached folders"
		base.IfWrong = "Work reaches a part of the machine you did not put in scope."
	case api.GateAgentChosenOutbound:
		base.What = withDetail(base.What, "the agent picked this destination — you did not configure it")
		base.IfWrong = "Content from this machine would reach a party you have no relationship with."
		// Cited when the chat has read external content. The destination is still
		// the subject; this says why it is worth attention now.
		if citedValue(decision, "session.state") != "" {
			base.What = withDetail(base.What, "this chat has read content this app did not author")
			base.IfWrong = "Instructions inside a page this chat read could be what is sending something here."
		}
	case api.GateSecretExposedOutbound:
		base.What = withDetail(base.What, "this chat has read a credential, and this request sends content out")
		base.IfWrong = "A value the agent read could leave in a form the outbound screen cannot recognize."
	case api.GateFirstHost:
		base.What = withDetail(base.What, "first connection to this host in this chat")
	case api.GateMCPUnleased:
		base.What = withDetail(base.What, "this MCP tool has no active approval")
	case api.GateUserRule:
		base.What = withDetail(base.What, "your approval rule asks about this")
	case api.GateExplicitApprovalRequest:
		base.What = withDetail(base.What, "this action explicitly requested your approval")
	case api.GateCapabilityWidening:
		base.What = withDetail(base.What, "this chat has not been granted this capability")
		base.IfWrong = "Commands in this chat can use the granted local-network axis until the chat is deleted."
	case api.GateIncompleteFacts:
		base.What = withDetail(base.What, "a safety check could not run for this action")
		base.IfWrong = "This is a fault in the app, not a judgement about the action. It asks rather than assuming the check would have passed."
	}
	return base
}

func withDetail(existing, detail string) string {
	detail = strings.TrimSpace(detail)
	existing = strings.TrimSpace(existing)
	switch {
	case detail == "":
		return existing
	case existing == "":
		return strings.ToUpper(detail[:1]) + detail[1:]
	default:
		return existing + " — " + detail
	}
}

func firstNonEmptyCopy(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// withBatchNote adds the remaining crossing count for batch actions.
func withBatchNote(what string, decision *gate.Decision) string {
	count := citedValue(decision, "file.other_count")
	if count == "" {
		return what
	}
	plural := "s"
	if count == "1" {
		plural = ""
	}
	note := "this batch also names " + count + " other path" + plural
	return withDetail(what, note)
}

// packageExecutionCopy states how many packages the one approval covers.
func packageExecutionCopy(packages int) (what, ifWrong string) {
	if packages > 1 {
		return "Download or run the " + strconv.Itoa(packages) +
				" package versions shown above without inherited credentials and with network access limited to their package registries.",
			"Package code can execute during this action; approving a different version of any of them requires another review."
	}
	return "Download or run the package version shown above without inherited credentials and with network access limited to its package registries.",
		"Package code can execute during this action; approving a different version requires another review."
}

func citedCount(decision *gate.Decision, key string) int {
	count := 0
	for _, f := range decision.Cited {
		if f.Key == key {
			count++
		}
	}
	return count
}

func citedValue(decision *gate.Decision, key string) string {
	for _, f := range decision.Cited {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}
