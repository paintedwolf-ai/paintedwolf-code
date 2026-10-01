package fileoutline

import (
	"strconv"
	"strings"
)

// Diff outlines need file boundaries, not a syntax tree for every added byte.
// Hunk lengths distinguish file headers from removed/added header-like content.
func outlineDiff(result *Result, text string) {
	result.Source = "diff"
	result.Language = "diff"
	oldLeft, newLeft := 0, 0
	oldPath, oldLine := "", 0
	gitIndex := -1
	combined := false
	row := 0
	for line := range strings.SplitSeq(text, "\n") {
		row++
		line = strings.TrimSuffix(line, "\r")
		if name, ok := diffGitPath(line); ok {
			result.Symbols = append(result.Symbols, Symbol{Kind: "section", Name: name, Line: row})
			gitIndex = len(result.Symbols) - 1
			oldPath = ""
			oldLeft, newLeft, combined = 0, 0, false
			continue
		}
		if strings.HasPrefix(line, "@@@ ") {
			combined = true
		}
		if combined {
			continue
		}
		if oldLeft > 0 || newLeft > 0 {
			if len(line) > 0 {
				switch line[0] {
				case ' ':
					oldLeft--
					newLeft--
				case '-':
					oldLeft--
				case '+':
					newLeft--
				case '\\':
					continue
				default:
					oldLeft, newLeft = 0, 0
				}
				if oldLeft >= 0 && newLeft >= 0 && (line[0] == ' ' || line[0] == '-' || line[0] == '+') {
					continue
				}
			}
			oldLeft, newLeft = 0, 0
		}
		if strings.HasPrefix(line, "@@ -") {
			fields := strings.Fields(line)
			if len(fields) >= 4 && fields[3] == "@@" {
				oldLeft = diffRangeLength(fields[1], '-')
				newLeft = diffRangeLength(fields[2], '+')
			}
			continue
		}

		if strings.HasPrefix(line, "--- ") {
			oldPath, oldLine = diffHeaderPath(line[4:]), row
			continue
		}
		if strings.HasPrefix(line, "+++ ") && oldPath != "" {
			name := diffHeaderPath(line[4:])
			if name == "/dev/null" {
				name = oldPath
			}
			if name != "" && name != "/dev/null" {
				name = strings.TrimPrefix(strings.TrimPrefix(name, "a/"), "b/")
				if gitIndex >= 0 {
					result.Symbols[gitIndex].Name = name
				} else {
					result.Symbols = append(result.Symbols, Symbol{Kind: "section", Name: name, Line: oldLine})
				}
			}
			oldPath, gitIndex = "", -1
		}
	}
}

func diffRangeLength(field string, prefix byte) int {
	if len(field) < 2 || field[0] != prefix {
		return 0
	}
	start, count, hasCount := strings.Cut(field[1:], ",")
	if _, err := strconv.ParseUint(start, 10, 32); err != nil {
		return 0
	}
	if !hasCount {
		return 1
	}
	n, err := strconv.ParseUint(count, 10, 32)
	if err != nil {
		return 0
	}
	return int(n)
}

func diffHeaderPath(raw string) string {
	name, _, _ := strings.Cut(raw, "\t")
	if strings.HasPrefix(name, `"`) {
		decoded, err := strconv.Unquote(name)
		if err != nil {
			return ""
		}
		return decoded
	}
	return name
}

func diffGitPath(line string) (string, bool) {
	for _, prefix := range []string{"diff --cc ", "diff --combined "} {
		if raw, ok := strings.CutPrefix(line, prefix); ok {
			name := diffHeaderPath(raw)
			return name, name != ""
		}
	}
	raw, ok := strings.CutPrefix(line, "diff --git ")
	if !ok {
		return "", false
	}
	// Git quotes each pathname separately when it contains control bytes.
	if strings.HasPrefix(raw, `"`) {
		for end := 1; end < len(raw); end++ {
			switch raw[end] {
			case '\\':
				end++
			case '"':
				name := diffHeaderPath(strings.TrimSpace(raw[end+1:]))
				return strings.TrimPrefix(name, "b/"), name != ""
			}
		}
		return "", false
	}
	if split := strings.LastIndex(raw, " b/"); split >= 0 {
		return raw[split+3:], true
	}
	// A quoted destination may follow an unquoted source.
	if split := strings.Index(raw, ` "`); split >= 0 {
		name := diffHeaderPath(raw[split+1:])
		return strings.TrimPrefix(name, "b/"), name != ""
	}
	return "", false
}
