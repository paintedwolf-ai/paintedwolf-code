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

var (
	emptyListItem  = regexp.MustCompile(`^\s*(\d+\.|[-*])\s*$`)
	spaceBeforeSep = regexp.MustCompile(` [,;]`)
	spaceBeforeDot = regexp.MustCompile(` \.`)
	inlineCode     = regexp.MustCompile("`[^`]*`")
	separatorCell  = regexp.MustCompile(`^:?-+:?$`)
)

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
		if emptyListItem.MatchString(line) {
			out = append(out, fmt.Sprintf("%s: an empty list item %q", at, trimmed))
		}
		if strings.Contains(line, "``") {
			out = append(out, fmt.Sprintf("%s: an empty backtick span %q", at, trimmed))
		}
		if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
			// Code spans carry literal syntax, so punctuation in them is not
			// drift. A letter stands in for each span: a space would fuse the
			// span's trailing punctuation into false drift.
			if punctuationDrift(inlineCode.ReplaceAllString(line, "x")) {
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

// punctuationDrift reports space before punctuation. A space before a dot
// that opens an uppercase token is a dotted name such as .NET.
func punctuationDrift(prose string) bool {
	if spaceBeforeSep.MatchString(prose) {
		return true
	}
	for _, loc := range spaceBeforeDot.FindAllStringIndex(prose, -1) {
		if loc[1] >= len(prose) || prose[loc[1]] < 'A' || prose[loc[1]] > 'Z' {
			return true
		}
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
