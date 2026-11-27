package promptrender

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
)

// KickTemplateIDs lists the guidance templates of a catalog's packs.
func KickTemplateIDs(eff *extpacks.EffectiveCatalog) ([]string, error) {
	packs, err := extpacks.DiscoverEffective(eff)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, dir := range extpacks.KindDirs(packs, "guidance") {
		entries, err := dir.List()
		if err != nil {
			return nil, err
		}
		for _, ent := range entries {
			if id, ok := strings.CutSuffix(ent.Name(), ".md"); ok && !ent.IsDir() && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return ids, nil
}

var separatorCell = regexp.MustCompile(`^:?-+:?$`)

// ProseDrift lists the structural damage in rendered prose: an empty list
// item, an empty backtick span, space before punctuation, a blanked table
// cell, and what prompts.RenderedProseStructureDrift finds.
func ProseDrift(text string) []string {
	var out []string
	inFence := false
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		at := fmt.Sprintf("line %d", i+1)
		if isEmptyListItem(line) {
			out = append(out, fmt.Sprintf("%s: an empty list item %q", at, trimmed))
		}
		if strings.Contains(line, "``") {
			out = append(out, fmt.Sprintf("%s: an empty backtick span %q", at, trimmed))
		}
		if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
			// Code spans carry literal syntax, so punctuation in them is not
			// drift. A letter stands in for each span: a space would fuse the
			// span's trailing punctuation into false drift.
			if punctuationDrift(replaceInlineCodeSpans(line)) {
				out = append(out, fmt.Sprintf("%s: space before punctuation %q", at, trimmed))
			}
			continue
		}
		if blankedCell(strings.Split(trimmed, "|")) {
			out = append(out, fmt.Sprintf("%s: a blanked table cell %q", at, trimmed))
		}
	}
	return append(out, prompts.RenderedProseStructureDrift(text)...)
}

// isEmptyListItem matches the ASCII-whitespace pattern `^\s*(\d+\.|[-*])\s*$`.
func isEmptyListItem(line string) bool {
	trimmed := strings.Trim(line, " \t\n\f\r")
	if trimmed == "-" || trimmed == "*" {
		return true
	}
	if len(trimmed) < 2 || trimmed[len(trimmed)-1] != '.' {
		return false
	}
	for i := 0; i < len(trimmed)-1; i++ {
		if trimmed[i] < '0' || trimmed[i] > '9' {
			return false
		}
	}
	return true
}

func replaceInlineCodeSpans(line string) string {
	first := strings.IndexByte(line, '`')
	if first < 0 {
		return line
	}
	var b strings.Builder
	b.Grow(len(line))
	rest := line
	for {
		open := strings.IndexByte(rest, '`')
		if open < 0 {
			b.WriteString(rest)
			break
		}
		closeIdx := strings.IndexByte(rest[open+1:], '`')
		if closeIdx < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:open])
		b.WriteByte('x')
		rest = rest[open+1+closeIdx+1:]
	}
	return b.String()
}

// punctuationDrift reports space before punctuation. A space before a dot
// that opens an uppercase token is a dotted name such as .NET.
func punctuationDrift(prose string) bool {
	if strings.Contains(prose, " ,") || strings.Contains(prose, " ;") {
		return true
	}
	for i := 0; i < len(prose); {
		idx := strings.Index(prose[i:], " .")
		if idx < 0 {
			return false
		}
		next := i + idx + 2
		if next >= len(prose) || prose[next] < 'A' || prose[next] > 'Z' {
			return true
		}
		i = next
	}
	return false
}

// blankedCell reports a whitespace-only cell in a row that is not a
// separator row.
func blankedCell(cells []string) bool {
	interior := cells[1 : len(cells)-1]
	if !slices.ContainsFunc(interior, func(cell string) bool { return !separatorCell.MatchString(strings.TrimSpace(cell)) }) {
		return false
	}
	return slices.ContainsFunc(interior, func(cell string) bool { return strings.TrimSpace(cell) == "" })
}
