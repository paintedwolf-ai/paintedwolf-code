// Package densource holds the invariants over Den source and user-read copy that
// name host vocabulary: they name only what the host defines, and never what
// Den must learn from a structured fact.
package densource

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

const settingsNavModel = "lycaon-den/src/settings/settings-nav-model.ts"

// Family is the den-source rule table.
var Family = invariants.Family{Name: "den-source", Invariants: []invariants.Invariant{
	{Name: "den-names-no-closeout-surface", Anchors: []invariants.Anchor{invariants.Func("internal/coordinator/surface", "SurfaceDeliversReport")}, Check: denNamesNoCloseoutSurface},
	{Name: "settings-pointers-name-real-sections", Anchors: []invariants.Anchor{invariants.File(settingsNavModel)}, Check: settingsPointersNameRealSections},
}}

// Den branches on CoordinatorLoopProgress.provisional_hidden, never on a
// closeout surface id; the closeout surfaces exist in the catalog.
func denNamesNoCloseoutSurface(r *invariants.Run) {
	closeout := []string{toolcontract.SurfaceImplementInvestigate, spawn.SurfaceImplementSynthesis}
	for _, id := range closeout {
		if !surface.SurfaceDeliversReport(id) {
			r.Report("%s is not a closeout surface", id)
		}
		if _, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: id}, 1); err != nil {
			r.Report("closeout surface %s is not in the surface catalog: %v", id, err)
		}
	}
	for _, f := range r.Tree("lycaon-den/src", ".ts", ".tsx") {
		if strings.Contains(f.Rel, ".test.") {
			continue
		}
		for _, id := range closeout {
			if strings.Contains(f.Text(), `"`+id+`"`) {
				r.Report("lycaon-den/src/%s names %s", f.Rel, id)
			}
		}
	}
}

var (
	navLabel      = regexp.MustCompile(`label:\s*"([^"]+)"`)
	navLabelConst = regexp.MustCompile(`(?m)^export const [A-Z_]+_LABEL\s*=\s*"([^"]+)"`)
)

// settingsTabs are second-level navigation labels by parent section.
var settingsTabs = map[string][]string{
	"General":   {"Display", "Notifications", "Editor", "Keyboard", "Power", "Updates", "About"},
	"Advanced":  {"Budgets", "Approvals", "Host resources", "Cache", "Data", "Diagnostics", "What projects may supply"},
	"Approvals": {"When we ask", "Saved approvals", "Detections"},
}

const (
	pointerLead = "Settings → "
	pointerSep  = " → "
)

// A "Settings → Section → Tab" pointer in stock packs or docs names a real
// navigation section, and a real tab where the section has tabs.
func settingsPointersNameRealSections(r *invariants.Run) {
	nav := r.Text(settingsNavModel)
	var sections []string
	for _, re := range []*regexp.Regexp{navLabel, navLabelConst} {
		for _, m := range re.FindAllStringSubmatch(nav, -1) {
			if !slices.Contains(sections, m[1]) {
				sections = append(sections, m[1])
			}
		}
	}
	if len(sections) < 10 {
		r.Fatalf("parsed only %d labels from %s", len(sections), settingsNavModel)
	}
	slices.Sort(sections)
	for _, dir := range []string{"lycaon/config/packs", "docs"} {
		for _, f := range r.Tree(dir, ".yaml", ".yml", ".md") {
			for _, p := range pointers(f.Text(), sections) {
				where := dir + "/" + f.Rel
				switch tabs, checked := settingsTabs[p.section]; {
				case !slices.Contains(sections, p.section):
					r.Report("%s: \"Settings → %s\" names no section; sections: %s", where, p.section, strings.Join(sections, ", "))
				case checked && p.tab != "" && !slices.Contains(tabs, p.tab):
					r.Report("%s: \"Settings → %s → %s\" names no tab under %s; tabs: %s", where, p.section, p.tab, p.section, strings.Join(tabs, ", "))
				}
			}
		}
	}
}

type pointer struct{ section, tab string }

// pointers finds the Settings pointers in copy. Emphasis and quoting are how
// a doc writes a name, not part of it.
func pointers(text string, sections []string) []pointer {
	text = strings.NewReplacer("**", "", "*", "", "`", "", "\"", "", "“", "", "”", "").Replace(text)
	var out []pointer
	for idx := 0; ; {
		hit := strings.Index(text[idx:], pointerLead)
		if hit < 0 {
			return out
		}
		at := idx + hit
		idx = at + len(pointerLead)
		// A chain rooted in an external settings surface is not Den's.
		if strings.HasSuffix(text[:at], "System ") || strings.HasSuffix(text[:at], pointerSep) {
			continue
		}
		rest := text[idx:]
		section, ok := longestPrefix(rest, sections)
		if !ok {
			out = append(out, pointer{section: firstWords(rest)})
			continue
		}
		p := pointer{section: section}
		if tabs, checked := settingsTabs[section]; checked {
			if tail, ok := strings.CutPrefix(rest[len(section):], pointerSep); ok {
				if tab, found := longestPrefix(tail, tabs); found {
					p.tab = tab
				} else {
					p.tab = firstWords(tail)
				}
			}
		}
		out = append(out, p)
	}
}

// longestPrefix prefers "Security scanners" over a shorter label that is its
// prefix.
func longestPrefix(s string, candidates []string) (string, bool) {
	best := ""
	for _, c := range candidates {
		if len(c) > len(best) && strings.HasPrefix(s, c) {
			best = c
		}
	}
	return best, best != ""
}

// firstWords bounds what follows an arrow to a name, not the paragraph.
func firstWords(s string) string {
	if cut := strings.IndexAny(s, ".,;:\n()"); cut >= 0 {
		s = s[:cut]
	}
	fields := strings.Fields(s)
	return strings.Join(fields[:min(len(fields), 4)], " ")
}
