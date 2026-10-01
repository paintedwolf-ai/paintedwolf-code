package packboard

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const (
	layoutFilesPrefix    = "Files: "
	layoutTopLevelPrefix = "Top-level: "
)

// FormatLayoutLine renders the tier-specific layout line (Files: or Top-level:).
// Returns empty when layout is absent, fails the quality floor, or has nothing to show.
func FormatLayoutLine(repo api.RepoBrief) string {
	names, prefix, budget, perNameCap := layoutLineInputs(repo)
	if len(names) == 0 || prefix == "" {
		return ""
	}
	eligible := filterLayoutNames(names, perNameCap)
	if len(eligible) == 0 {
		return ""
	}
	line, shown := renderLayoutLine(prefix, eligible, budget)
	if !layoutQualityFloor(shown, len(eligible)) {
		return ""
	}
	return line
}

func layoutLineInputs(repo api.RepoBrief) (names []string, prefix string, budget int, perNameCap int) {
	switch {
	case repo.FileCount >= 1 && repo.FileCount <= TinyMaxFiles:
		names = repo.Layout.Files
		prefix = layoutFilesPrefix
		budget = MaxLayoutLineBytes
		perNameCap = MaxLayoutLineBytes / 3
	case repo.FileCount > TinyMaxFiles && repo.FileCount <= SmallMaxFiles:
		names = repo.Layout.TopLevel
		prefix = layoutTopLevelPrefix
		budget = MaxLayoutLineBytes
		perNameCap = MaxLayoutLineBytes / 3
	case repo.FileCount > SmallMaxFiles && repo.FileCount <= MediumMaxFiles:
		names = repo.Layout.TopLevel
		prefix = layoutTopLevelPrefix
		budget = MaxMediumLayoutLineBytes
		perNameCap = MaxMediumLayoutLineBytes / 3
	default:
		return nil, "", 0, 0
	}
	return names, prefix, budget, perNameCap
}

func filterLayoutNames(names []string, perNameCap int) []string {
	if perNameCap <= 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || len(name) > perNameCap {
			continue
		}
		out = append(out, name)
	}
	return out
}

func layoutQualityFloor(shown, eligible int) bool {
	if eligible == 0 {
		return false
	}
	if shown >= 3 {
		return true
	}
	return shown*2 >= eligible
}

func renderLayoutLine(prefix string, eligible []string, budget int) (line string, shown int) {
	if budget <= len(prefix) {
		return "", 0
	}
	remaining := budget - len(prefix)
	var parts []string
	for _, name := range eligible {
		candidate := name
		if len(parts) > 0 {
			candidate = strings.Join(append(parts, name), ", ")
		}
		if len(candidate) > remaining {
			break
		}
		parts = append(parts, name)
	}
	shown = len(parts)
	if shown == 0 {
		return "", 0
	}
	line = prefix + strings.Join(parts, ", ")
	rest := len(eligible) - shown
	if rest > 0 {
		suffix := fmt.Sprintf(" +%d more", rest)
		if len(line)+len(suffix) <= budget {
			line += suffix
		}
	}
	return line, shown
}

func isLayoutLine(line string) bool {
	return strings.HasPrefix(line, layoutFilesPrefix) || strings.HasPrefix(line, layoutTopLevelPrefix)
}

func dropLayoutLine(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if isLayoutLine(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}

func isToolchainsLine(line string) bool {
	return strings.HasPrefix(line, "Toolchains:")
}

func dropToolchainsLine(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if isToolchainsLine(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}

// FitOrientationBudget truncates orientation lines to max. Drops layout, flags, and toolchains
// lines before bottom-up truncation when the body would exceed the inject ceiling.
func FitOrientationBudget(lines []string, max int, truncated *[]string) ([]string, bool) {
	if max <= 0 {
		max = api.MaxBoardInjectChars
	}
	if len(strings.Join(lines, "\n")) <= max {
		return lines, false
	}
	var droppedOptional bool
	if trimmed := dropLayoutLine(lines); len(trimmed) < len(lines) {
		lines = trimmed
		droppedOptional = true
		if len(strings.Join(lines, "\n")) <= max {
			return lines, droppedOptional
		}
	}
	if trimmed := dropFlagsLine(lines); len(trimmed) < len(lines) {
		lines = trimmed
		droppedOptional = true
		if len(strings.Join(lines, "\n")) <= max {
			return lines, droppedOptional
		}
	}
	if trimmed := dropToolchainsLine(lines); len(trimmed) < len(lines) {
		lines = trimmed
		droppedOptional = true
		if len(strings.Join(lines, "\n")) <= max {
			return lines, droppedOptional
		}
	}
	lines, didTruncate := TruncateLinesBottomUp(lines, max, truncated)
	return lines, droppedOptional || didTruncate
}
