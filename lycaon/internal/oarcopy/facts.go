// Package oarcopy maps host occurrence keys onto declared copy-fact names.
package oarcopy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/oarcore"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

const HostFactNamespace = "paintedwolf"

// CapabilitySnapshotKey carries typed host facts only during local formatting.
const CapabilitySnapshotKey = "_host_capability_snapshot"

// Standard profile names ([OAR-FACT-13]).
const (
	ProfileFilesystem    = "filesystem"
	ProfileContentSafety = "content-safety"
	ProfileMCP           = "mcp"
)

var (
	requiredCoreFacts     = sortedKeys(oarcore.CoreFacts())
	requiredCoreFunctions = sortedKeys(oarcore.CoreFunctions())
	standardProfileFacts  = buildStandardProfileFacts()
)

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// buildStandardProfileFacts lists each profile's facts and functions.
func buildStandardProfileFacts() map[string][]string {
	out := map[string][]string{}
	for name, def := range oarcore.StandardProfiles() {
		out[name] = append(sortedKeys(def.Facts), sortedKeys(def.Functions)...)
	}
	return out
}

// RequiredCoreFacts returns the normative core facts.
func RequiredCoreFacts() []string {
	return append([]string(nil), requiredCoreFacts...)
}

// RequiredCoreFunctions returns the normative core observation functions.
func RequiredCoreFunctions() []string {
	return append([]string(nil), requiredCoreFunctions...)
}

// StandardProfileFacts returns each standard profile and its complete membership.
func StandardProfileFacts() map[string][]string {
	out := make(map[string][]string, len(standardProfileFacts))
	for k, v := range standardProfileFacts {
		out[k] = append([]string(nil), v...)
	}
	return out
}

var profileNames = buildPortableNames()

func buildPortableNames() map[string]bool {
	out := make(map[string]bool, len(requiredCoreFacts)+32)
	for _, name := range requiredCoreFacts {
		out[name] = true
	}
	for _, name := range requiredCoreFunctions {
		out[name] = true
	}
	for _, members := range standardProfileFacts {
		for _, name := range members {
			out[name] = true
		}
	}
	return out
}

// IsPortableFact reports whether name is a core or standard-profile identifier
// published without the host namespace prefix.
func IsPortableFact(name string) bool {
	return profileNames[strings.TrimSpace(name)]
}

var publishedFactNames = map[string]string{
	"reason":  "paintedwolf.rejection_reason",
	"field":   "paintedwolf.rejection_field",
	"profile": "paintedwolf.tool_profile",
}

var reasonFlags = map[string]string{
	"unescaped_newline":   "paintedwolf.json_unescaped_newline",
	"unexpected_end":      "paintedwolf.json_unexpected_end",
	"invalid_escape":      "paintedwolf.json_invalid_escape",
	"reference_required":  "paintedwolf.reference_required",
	"grounding_escalated": "paintedwolf.grounding_escalated",
	"bare_repo_scope":     "paintedwolf.bare_repo_scope",
	"unpaginated_survey":  "paintedwolf.unpaginated_survey",
	"opaque_overflow":     "paintedwolf.opaque_overflow",
}

// FactsFromData rewrites occurrence keys onto declared fact names and
// derives the boolean flags copy can test.
func FactsFromData(data map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range data {
		if key != CapabilitySnapshotKey {
			out[publishKey(key)] = value
		}
	}
	publishParserFacts(out)
	tool, _ := data["tool"].(string)
	args, _ := data["tool_args"].(map[string]any)
	snapshot, observed := data[CapabilitySnapshotKey].(toolcontract.ToolCapabilitySnapshot)
	var contract *toolcontract.ToolCapabilitySnapshot
	if observed {
		contract = &snapshot
	}
	PublishInvocationRecovery(out, tool, args, contract)
	PublishDiagnosticKnowledge(out, data)
	publishSocketReferences(out, data)
	reason, _ := out["paintedwolf.rejection_reason"].(string)
	if reason == "" {
		if raw, ok := data["reason"]; ok {
			reason = fmt.Sprint(raw)
			if reason == "<nil>" {
				reason = ""
			}
		}
	}
	if reason != "" {
		out["paintedwolf.rejection_reason"] = reason
		if flag, ok := reasonFlags[reason]; ok {
			out[flag] = true
		}
	}
	if kind, ok := data["kind"].(string); ok && kind == "redirect" {
		out["paintedwolf.redirect"] = true
	}
	if operation, _ := out["paintedwolf.native_operation"].(string); operation != "" {
		if form := CommandFormOf(operation); form != "" {
			out["paintedwolf.command_form"] = form
		}
	}
	return out
}

// CommandFormOf returns the single catalog command spelling for an operation
// or form-tool id. Operations with several command forms (or none) have no
// canonical spelling and map to "".
func CommandFormOf(operation string) string {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return ""
	}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return ""
	}
	for _, disposition := range catalog.Tools {
		if disposition.Tool == operation && len(disposition.CommandForms) == 1 {
			return disposition.CommandForms[0]
		}
	}
	return ""
}

func publishKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return key
	}
	if name, ok := publishedFactNames[key]; ok {
		return name
	}
	if profileNames[key] || strings.Contains(key, ".") {
		return key
	}
	return HostFactNamespace + "." + key
}
