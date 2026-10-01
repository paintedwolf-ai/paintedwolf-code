package exec

import (
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// blockedEnvKeys cannot cross into child processes.
var blockedEnvKeys = map[string]struct{}{
	"BASH_ENV":      {},
	"ENV":           {},
	"NODE_OPTIONS":  {},
	"PERL5OPT":      {},
	"RUBYOPT":       {},
	"SSLKEYLOGFILE": {},
}

// blockedEnvPrefixes are matched case-insensitively.
// LYCAON_ prefixes hold process-specific state stripped before child dispatch.
var blockedEnvPrefixes = []string{
	"LD_",
	"DYLD_",
	"GIT_",
	"LYCAON_",
}

// inlineBlockedEnvKeys are refused for inline env to prevent loader hijacking.
var inlineBlockedEnvKeys = map[string]struct{}{
	"PATH": {},
}

// SanitizeEnviron removes host injection and hook variables.
// A non-nil empty slice is preserved to prevent os/exec from inheriting the parent environment.
func SanitizeEnviron(entries []string) []string {
	if entries == nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		upper := strings.ToUpper(key)
		if _, blocked := blockedEnvKeys[upper]; blocked {
			continue
		}
		if hasBlockedPrefix(upper) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func hasBlockedPrefix(upperKey string) bool {
	for _, prefix := range blockedEnvPrefixes {
		if strings.HasPrefix(upperKey, prefix) {
			return true
		}
	}
	return false
}

func resolveEnv(provided []string) []string {
	if provided != nil {
		return SanitizeEnviron(provided)
	}
	return InheritedEnviron()
}

// InheritedEnviron returns sanitized variables with the resolved user PATH.
func InheritedEnviron() []string {
	return withResolvedPath(SanitizeEnviron(os.Environ()))
}

// ReducedEnviron returns process plumbing without ambient credentials.
func ReducedEnviron() []string {
	allowed := map[string]struct{}{
		"PATH": {},
		"HOME": {}, "TMPDIR": {}, "TMP": {}, "TEMP": {},
		"LANG": {}, "LANGUAGE": {}, "TERM": {}, "COLORTERM": {}, "NO_COLOR": {},
	}
	var out []string
	for _, entry := range SanitizeEnviron(os.Environ()) {
		key, val, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(strings.TrimSpace(key))
		if _, keep := allowed[upper]; keep || strings.HasPrefix(upper, "LC_") {
			if val != "" && (upper == "TMPDIR" || upper == "TMP" || upper == "TEMP") {
				if canonical := fspath.CanonicalPath(val); canonical != "" {
					entry = key + "=" + canonical
				}
			}
			out = append(out, entry)
		}
	}
	return withResolvedPath(out)
}

func withResolvedPath(entries []string) []string {
	value := ResolvedPathValue()
	if value == "" {
		return entries
	}
	out := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		if key, _, ok := strings.Cut(entry, "="); ok && key == "PATH" {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "PATH="+value)
}
