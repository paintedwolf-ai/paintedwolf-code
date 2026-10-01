package contract

import (
	"fmt"
	"sort"
	"strings"
)

// promptBudgetMeasurements maps budget category → artifact id → rendered UTF-8 byte size.
type promptBudgetMeasurements map[string]map[string]int

type promptBudgetViolationKind string

const (
	promptBudgetOverCap    promptBudgetViolationKind = "over_cap"
	promptBudgetMissingCap promptBudgetViolationKind = "missing_cap"
	promptBudgetStaleCap   promptBudgetViolationKind = "stale_cap"
)

type promptBudgetViolation struct {
	Category string
	ID       string
	Kind     promptBudgetViolationKind
	Measured int
	Cap      int
}

func collectPromptBudgetViolations(
	measured promptBudgetMeasurements,
	budgets map[string]map[string]int,
) []promptBudgetViolation {
	var out []promptBudgetViolation
	for category, sizes := range measured {
		caps := budgets[category]
		for id, size := range sizes {
			cap, ok := caps[id]
			if !ok {
				out = append(out, promptBudgetViolation{
					Category: category,
					ID:       id,
					Kind:     promptBudgetMissingCap,
					Measured: size,
				})
				continue
			}
			if size > cap {
				out = append(out, promptBudgetViolation{
					Category: category,
					ID:       id,
					Kind:     promptBudgetOverCap,
					Measured: size,
					Cap:      cap,
				})
			}
		}
		for id := range caps {
			if _, ok := sizes[id]; !ok {
				out = append(out, promptBudgetViolation{
					Category: category,
					ID:       id,
					Kind:     promptBudgetStaleCap,
					Cap:      caps[id],
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func formatPromptBudgetFailureReport(violations []promptBudgetViolation, catalog map[string]map[string]promptBudgetEntry) string {
	if len(violations) == 0 {
		return ""
	}
	categories := promptBudgetCategories

	var b strings.Builder
	fmt.Fprintf(&b, "prompt budget: %d violation(s)\n", len(violations))
	b.WriteString("Caps are UTF-8 byte counts of fully rendered production prompts (SSOT: lycaon/config/packs/painted-wolf/platform/host/prompt-budgets.yaml).\n")
	b.WriteString("Prefer TRIM (shorten prompt copy) over BUMP (raising caps). Bumping without trimming hides tripartite bloat.\n\n")

	for _, v := range violations {
		cat := categories[v.Category]
		entry, hasEntry := catalog[v.Category][v.ID]
		fmt.Fprintf(&b, "── %s / %s ──\n", v.Category, v.ID)
		if cat.Title != "" {
			fmt.Fprintf(&b, "category: %s — %s\n", cat.Title, cat.Description)
		}
		if cat.CapPath != "" {
			b.WriteString("yaml key: " + promptBudgetYAMLKey(cat.CapPath, v.ID) + "\n")
		}

		switch v.Kind {
		case promptBudgetOverCap:
			over := v.Measured - v.Cap
			pct := 100 * float64(over) / float64(v.Cap)
			fmt.Fprintf(&b, "measured: %d bytes | cap: %d bytes | over by: %d bytes (+%.1f%%)\n", v.Measured, v.Cap, over, pct)
		case promptBudgetMissingCap:
			fmt.Fprintf(&b, "measured: %d bytes | cap: (missing — add to prompt-budgets.yaml)\n", v.Measured)
		case promptBudgetStaleCap:
			fmt.Fprintf(&b, "stale cap: %d bytes in yaml but fixture no longer rendered — delete this key\n", v.Cap)
		}

		if hasEntry {
			if entry.Measures != "" {
				b.WriteString("measures: " + entry.Measures + "\n")
			}
			if entry.Fixture != "" {
				b.WriteString("fixture: " + entry.Fixture + "\n")
			}
			if len(entry.TrimPaths) > 0 {
				b.WriteString("trim (edit these first):\n")
				for _, p := range entry.TrimPaths {
					b.WriteString("  - " + p + "\n")
				}
			}
			if entry.BumpNote != "" {
				b.WriteString("bump note: " + entry.BumpNote + "\n")
			}
		}

		switch v.Kind {
		case promptBudgetOverCap:
			b.WriteString("fix: shorten trim paths above, OR raise the cap in prompt-budgets.yaml\n")
		case promptBudgetMissingCap:
			b.WriteString("fix: add cap under prompt-budgets.yaml, or refresh all measured caps:\n")
		case promptBudgetStaleCap:
			b.WriteString("fix: remove stale key from prompt-budgets.yaml\n")
		}
		if cat.RefreshCmd != "" && v.Kind != promptBudgetStaleCap {
			b.WriteString("refresh all caps: " + cat.RefreshCmd + "\n")
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func promptBudgetYAMLKey(capPath, id string) string {
	key := capPath
	for _, placeholder := range []string{"<fixture_name>", "<agent_id>", "<inject_id>", "<profile_id>", "<kick_id>"} {
		key = strings.ReplaceAll(key, placeholder, id)
	}
	return key
}
