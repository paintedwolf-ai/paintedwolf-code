package invocation

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Phase is the typed fold and headline key; group is its display name.
type activityRule struct{ role, group, label, phase string }

func activityPresentation(input activityInput) activityRule {
	receipt := input.receipt
	if input.operation == "program &" {
		return activityRule{"primary", "Running programs", "Start background job", "execute"}
	}
	if strings.HasPrefix(input.operation, "program ") {
		return activityRule{"container", "Running commands", "Run command", "execute"}
	}
	if receipt.EffectMode == "external" || receipt.NativeCommand == "external" || (input.operation == "command" && receipt.Owner == "processes") {
		return activityRule{"primary", "Running programs", "Run process", "execute"}
	}
	if input.operation == "cd" {
		return activityRule{"context", "Exploring project files", "Change directory", "inspect"}
	}
	if input.operation == "find" || input.operation == "list_dir" {
		return activityRule{"primary", "Exploring project files", "Search paths", "inspect"}
	}
	switch input.operation {
	case "git_commit":
		return activityRule{"primary", "Recording changes", "Create commit", "mutate"}
	case "git_add", "git add":
		return activityRule{"primary", "Recording changes", "Stage changes", "mutate"}
	}
	if strings.HasPrefix(input.operation, "git") || input.family == "git" {
		if receipt.Lifecycle == "read_only" || hasActivityClass(receipt, "read_only") {
			return activityRule{"primary", "Inspecting version history", "Inspect Git", "inspect"}
		}
		return activityRule{"primary", "Recording changes", "Change Git state", "mutate"}
	}
	if hasActivityClass(receipt, "mutation") {
		return activityRule{"primary", "Changing files", "Change files", "mutate"}
	}
	if hasActivityClass(receipt, "network_egress") {
		return activityRule{"primary", "Accessing the network", "Access network", "execute"}
	}
	reads := activityPaths(input.arguments, "read")
	switch input.family {
	case "search", "query":
		if len(reads) > 0 {
			return activityRule{"primary", "Exploring project files", "Search contents", "inspect"}
		}
	case "inspect", "sed", "metadata":
		if len(reads) > 0 {
			return activityRule{"primary", "Exploring project files", "Read source", "inspect"}
		}
	}
	if receipt.Lifecycle == "read_only" && len(reads) > 0 {
		return activityRule{"primary", "Exploring project files", "Read source", "inspect"}
	}
	if receipt.Lifecycle == "read_only" || hasActivityClass(receipt, "read_only") {
		if input.pipeline || len(reads) == 0 {
			return activityRule{"support", "Processing output", "Process output", "inspect"}
		}
		return activityRule{"primary", "Processing output", "Process output", "inspect"}
	}
	return activityRule{"primary", "Running commands", "Run command", "execute"}
}

// Steps read as actions: label with subject when the operation has one.
func activityStepLabel(g activityGroup) string {
	rule := activityPresentation(g.input)
	if subject := activitySubject(g.input); subject != "" {
		return boundedActivityText(rule.label + " · " + subject)
	}
	if command := g.input.receipt.Command; command != "" {
		return boundedActivityText(command)
	}
	return rule.label
}

func hasActivityClass(receipt api.CommandExecutionReceipt, class string) bool {
	for _, value := range receipt.EffectClasses {
		if value == class {
			return true
		}
	}
	return false
}

func stringArgument(arguments map[string]any, name string) string {
	value, _ := arguments[name].(string)
	return value
}

func activityPaths(arguments map[string]any, role string) []string {
	targets, _ := arguments["path_targets"].(map[string]any)
	paths, _ := targets[role].([]any)
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if value, ok := path.(string); ok {
			result = append(result, value)
		}
	}
	if len(result) == 0 && role == "read" {
		if path := stringArgument(arguments, "path"); path != "" {
			result = append(result, path)
		}
	}
	return result
}

func activitySubject(input activityInput) string {
	if input.operation == "program &" {
		return ""
	}
	if query := stringArgument(input.arguments, "query"); query != "" {
		return boundedActivityText(query)
	}
	if pattern := stringArgument(input.arguments, "pattern"); pattern != "" {
		return boundedActivityText(pattern)
	}
	if url := stringArgument(input.arguments, "url"); url != "" {
		return boundedActivityText(url)
	}
	if message := stringArgument(input.arguments, "message"); message != "" {
		return boundedActivityText(message)
	}
	if activityPresentation(input).label == "Run process" {
		if command := stringArgument(input.arguments, "command"); command != "" {
			return boundedActivityText(command)
		}
	}
	for _, role := range []string{"write", "read"} {
		if paths := activityPaths(input.arguments, role); len(paths) > 0 {
			if len(paths) > 1 {
				return boundedActivityText(fmt.Sprintf("%s · multiple paths", paths[0]))
			}
			return boundedActivityText(paths[0])
		}
	}
	return boundedActivityText(input.receipt.Command)
}

func boundedActivityText(value string) string {
	const maximum = 256
	runes := []rune(value)
	if len(runes) > maximum {
		return string(runes[:maximum]) + "…"
	}
	return value
}
