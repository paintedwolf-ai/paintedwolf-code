package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Settings pointers must name an existing navigation section.
const settingsNavModelRel = "lycaon-den/src/settings/settings-nav-model.ts"

// settingsPointerRoots are the trees whose copy a user or an agent actually reads.
var settingsPointerRoots = []string{
	"lycaon/config/packs",
	"docs",
}

var (
	reNavLabel = regexp.MustCompile(`label:\s*"([^"]+)"`)
	// Shared labels are read from their declarations.
	reNavLabelConst = regexp.MustCompile(`(?m)^export const [A-Z_]+_LABEL\s*=\s*"([^"]+)"`)
)

// settingsPointerLead marks a Settings navigation reference.
const settingsPointerLead = "Settings → "

const settingsPointerSep = " → "

// settingsSubTabs are second-level navigation labels by parent.
var settingsSubTabs = map[string][]string{
	"General":   {"Display", "Notifications", "Editor", "Keyboard", "Power", "Updates", "About"},
	"Advanced":  {"Budgets", "Approvals", "Host resources", "Cache", "Data", "Diagnostics", "What projects may supply"},
	"Approvals": {"When we ask", "Saved approvals", "Detections"},
}

func TestSettingsPointersNameRealSections(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	raw, err := os.ReadFile(filepath.Join(root, settingsNavModelRel))
	contractcheck.FailErr(t, "read settings nav model", err)
	nav := string(raw)

	sections := map[string]bool{}
	for _, m := range reNavLabel.FindAllStringSubmatch(nav, -1) {
		sections[m[1]] = true
	}
	for _, m := range reNavLabelConst.FindAllStringSubmatch(nav, -1) {
		sections[m[1]] = true
	}
	if len(sections) < 10 {
		t.Fatalf("parsed only %d labels from %s — the parse broke, not the copy", len(sections), settingsNavModelRel)
	}

	known := sortedKeys(sections)
	var problems []string
	for _, rel := range settingsPointerRoots {
		walkSettingsPointers(t, root, rel, known, func(file, first, second string) {
			if !sections[first] {
				problems = append(problems, file+": \"Settings → "+first+
					"\" names no section. Sections: "+strings.Join(known, ", "))
				return
			}
			tabs, checked := settingsSubTabs[first]
			if !checked || second == "" {
				return
			}
			for _, tab := range tabs {
				if tab == second {
					return
				}
			}
			problems = append(problems, file+": \"Settings → "+first+" → "+second+
				"\" names no tab under "+first+". Tabs: "+strings.Join(tabs, ", "))
		})
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

func walkSettingsPointers(t *testing.T, root, rel string, sections []string, visit func(file, first, second string)) {
	t.Helper()
	base := filepath.Join(root, rel)
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml", ".md":
		default:
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		// Emphasis and quoting are how a doc writes a name, not part of the name:
		// "Settings → **Cost**" points at the same section as "Settings → Cost".
		text := stripCopyDecoration(string(body))
		display, relErr := filepath.Rel(root, path)
		if relErr != nil {
			display = path
		}
		for idx := 0; ; {
			hit := strings.Index(text[idx:], settingsPointerLead)
			if hit < 0 {
				return nil
			}
			at := idx + hit
			idx = at + len(settingsPointerLead)
			// Ignore chains already rooted in an external settings surface.
			if strings.HasSuffix(text[:at], "System ") || strings.HasSuffix(text[:at], settingsPointerSep) {
				continue
			}
			rest := text[idx:]
			first, ok := longestLabelPrefix(rest, sections)
			if !ok {
				visit(display, firstWordsOf(rest), "")
				continue
			}
			second := ""
			if tabs, checked := settingsSubTabs[first]; checked {
				tail := rest[len(first):]
				if strings.HasPrefix(tail, settingsPointerSep) {
					after := tail[len(settingsPointerSep):]
					if match, found := longestLabelPrefix(after, tabs); found {
						second = match
					} else {
						second = firstWordsOf(after)
					}
				}
			}
			visit(display, first, second)
		}
	})
	contractcheck.FailErr(t, "walk "+rel, err)
}

// longestLabelPrefix returns the longest candidate that s starts with, so "Security
// scanners" is preferred over a shorter label that happens to be a prefix of it.
func longestLabelPrefix(s string, candidates []string) (string, bool) {
	best := ""
	for _, c := range candidates {
		if len(c) > len(best) && strings.HasPrefix(s, c) {
			best = c
		}
	}
	return best, best != ""
}

// firstWordsOf renders what someone actually wrote after the arrow, bounded so a failure
// message quotes a name rather than the rest of the paragraph.
func firstWordsOf(s string) string {
	if cut := strings.IndexAny(s, ".,;:\n()"); cut >= 0 {
		s = s[:cut]
	}
	fields := strings.Fields(s)
	if len(fields) > 4 {
		fields = fields[:4]
	}
	return strings.Join(fields, " ")
}

// stripCopyDecoration removes markdown emphasis and quoting so a pointer is compared as
// the words someone would read aloud.
func stripCopyDecoration(s string) string {
	return strings.NewReplacer(
		"**", "", "*", "", "`", "", "\"", "", "\u201c", "", "\u201d", "",
	).Replace(s)
}
