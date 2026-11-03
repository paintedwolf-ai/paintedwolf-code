package privategit

import (
	"bytes"
	"strings"
)

var forbiddenCoreKeys = map[string]bool{
	"worktree":       true,
	"gitdir":         true,
	"hookspath":      true,
	"fsmonitor":      true,
	"gitproxy":       true,
	"sshcommand":     true,
	"askpass":        true,
	"pager":          true,
	"editor":         true,
	"attributesfile": true,
	"excludesfile":   true,
}

var forbiddenRemoteKeys = map[string]bool{
	"uploadpack":  true,
	"receivepack": true,
	"proxy":       true,
	"vcs":         true,
}

// SanitizeGitConfig strips configuration sections and keys that could redirect
// metadata/working-tree writes (`core.worktree`, `core.gitdir`, `include.path`,
// `extensions.worktreeConfig`) or execute arbitrary commands (`core.hooksPath`,
// `core.fsmonitor`, `core.sshCommand`, `filter.*`, `alias.*`, `credential.helper`).
func SanitizeGitConfig(raw []byte) []byte {
	var out bytes.Buffer
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")

	curSection := ""
	curSub := ""
	skipSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			if !skipSection {
				out.WriteString(line)
				out.WriteByte('\n')
			}
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			sec, sub := parseIniHeader(trimmed)
			curSection = strings.ToLower(sec)
			curSub = sub
			skipSection = shouldSkipSection(curSection, curSub)
			if !skipSection {
				out.WriteString(line)
				out.WriteByte('\n')
			}
			continue
		}
		if skipSection {
			continue
		}
		key, _, hasEq := strings.Cut(trimmed, "=")
		keyLower := strings.ToLower(strings.TrimSpace(key))
		if !hasEq {
			keyLower = strings.ToLower(trimmed)
		}
		if shouldSkipKey(curSection, keyLower) {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}

	out.WriteString("[core]\n\tbare = false\n\tlogallrefupdates = true\n[gc]\n\tauto = 0\n")
	return out.Bytes()
}

func parseIniHeader(header string) (string, string) {
	inner := strings.TrimSpace(header[1 : len(header)-1])
	if idx := strings.IndexByte(inner, '"'); idx >= 0 {
		sec := strings.TrimSpace(inner[:idx])
		sub := strings.Trim(inner[idx:], `" `)
		return sec, sub
	}
	fields := strings.Fields(inner)
	if len(fields) == 0 {
		return "", ""
	}
	if len(fields) == 1 {
		return fields[0], ""
	}
	return fields[0], strings.Join(fields[1:], " ")
}

func shouldSkipSection(section, _ string) bool {
	switch section {
	case "include", "includeif", "alias", "filter", "credential", "protocol":
		return true
	default:
		return false
	}
}

func shouldSkipKey(section, key string) bool {
	switch section {
	case "core":
		return forbiddenCoreKeys[key]
	case "extensions":
		return key == "worktreeconfig"
	case "remote":
		return forbiddenRemoteKeys[key]
	case "merge":
		return key == "driver" || key == "tool"
	case "diff":
		return key == "external" || key == "command" || key == "textconv"
	case "commit", "tag":
		return key == "gpgsign"
	default:
		return false
	}
}
