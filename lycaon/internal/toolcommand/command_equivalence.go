package toolcommand

//go:generate go run ../../cmd/codegen-tool-command-equivalence --root ../..

import (
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"strings"
)

// commandEquivalenceRunners accept free-form commands that can be translated
// losslessly into native calls before execution.
var commandEquivalenceRunners = map[string]struct{}{
	"command": {},
	"verify":  {},
}

func IsCommandEquivalenceRunner(toolName string) bool {
	_, ok := commandEquivalenceRunners[toolName]
	return ok
}

func commandEquivalenceEntryFor(native string) (commandEquivalenceEntry, bool) {
	for _, entry := range commandEquivalenceEntries {
		if entry.Native == native {
			return entry, true
		}
	}
	return commandEquivalenceEntry{}, false
}

// IsNativeCommandRedirectCode reports catalog-authored retry guidance, which
// does not count as a blocked execution loop.
func IsNativeCommandRedirectCode(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	for _, entry := range commandEquivalenceEntries {
		if entry.RedirectCode == code {
			return true
		}
	}
	return false
}

// NativeReplacesCommandSuffix returns the profile-scoped replaces line for a native tool.
func NativeReplacesCommandSuffix(toolName, profileID string) string {
	entry, ok := commandEquivalenceEntryFor(toolName)
	if !ok {
		return ""
	}
	if profileID != "" && entry.Profiles != nil && !entry.Profiles[profileID] {
		return ""
	}
	return entry.ReplacesLine
}

// CommandInverseSurveyLine lists native survey tools reserved for implementers.
func CommandInverseSurveyLine(profileID string) string {
	if profileID != toolprofiles.DefaultToolProfileID {
		return ""
	}
	if len(commandSurveyNativeTools) == 0 {
		return ""
	}
	return "Do not use for: " + strings.Join(commandSurveyNativeTools, ", ") + " (use native tools)."
}

func AppendReplacesSuffix(description, suffix string) string {
	base := stripReplacesCommandSuffix(description)
	if suffix == "" {
		return base
	}
	return base + suffix
}

// replacesCommandPrefix heads the generated replaces line in a tool description.
const replacesCommandPrefix = " Replaces command:"

func stripReplacesCommandSuffix(desc string) string {
	if i := strings.Index(desc, replacesCommandPrefix); i >= 0 {
		return strings.TrimSpace(desc[:i])
	}
	return strings.TrimSpace(desc)
}
