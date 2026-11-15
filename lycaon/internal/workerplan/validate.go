// Package workerplan seeds planned workers from approved Blueprints, enforces
// plan and overlap gates at round admission, evaluates typed criteria on round
// delivery, and checks plan settlement before closeout.
package workerplan

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	tasksMarkerStart = "<!-- lycaon:tasks"
	tasksMarkerEnd   = "-->"
	defaultLegWorkID = "plan-1"
	maxWorkIDLen     = 64
)

var defaultCriteria = []string{"job:complete", "summary:present"}

// LegSpec is one validated Blueprint task ready to reconcile into workerrecords.
type LegSpec struct {
	WorkID    string
	Title     string
	Brief     string
	AgentType string
	ScopeMode string
	Files     []string
	DependsOn []string
	Criteria  []string
}

// ParseBlueprintLegs extracts and validates the task graph embedded in plan.
// A Blueprint with no task block produces one default implementation leg; a
// malformed task block, cycle, unknown dependency, or unordered write-file
// overlap returns an error.
func ParseBlueprintLegs(plan *api.Blueprint) ([]LegSpec, error) {
	var content, title string
	if plan != nil {
		content = plan.Content
		title = strings.TrimSpace(plan.Title)
	}
	tasks, hasMarker, err := extractStrictTasks(content)
	if err != nil {
		return nil, err
	}
	if !hasMarker || len(tasks) == 0 {
		brief := "Implement approved plan"
		if title != "" {
			brief = "Implement plan: " + guidance.StripHostBlocks(title)
		}
		return []LegSpec{{
			WorkID:    defaultLegWorkID,
			Title:     "Worker leg",
			Brief:     brief,
			AgentType: orchestration.ProfileImplementer,
			ScopeMode: workerrecords.ScopeWrite,
			Files:     []string{"**/*"},
			Criteria:  append([]string(nil), defaultCriteria...),
		}}, nil
	}
	legs := make([]LegSpec, 0, len(tasks))
	for _, task := range tasks {
		id := task.ID
		trimmedID := strings.TrimSpace(id)
		if trimmedID == "" || trimmedID != id || utf8.RuneCountInString(id) > maxWorkIDLen {
			return nil, fmt.Errorf("invalid blueprint task id %q", id)
		}
		legTitle := strings.TrimSpace(guidance.StripHostBlocks(task.Title))
		if legTitle == "" {
			legTitle = "Plan task"
		}
		files := cleanPaths(task.Files)
		brief := legTitle
		if len(files) > 0 {
			brief = fmt.Sprintf("%s (files: %s)", legTitle, strings.Join(files, ", "))
		}
		legs = append(legs, LegSpec{
			WorkID:    id,
			Title:     legTitle,
			Brief:     brief,
			AgentType: orchestration.ProfileImplementer,
			ScopeMode: workerrecords.ScopeWrite,
			Files:     files,
			DependsOn: append([]string(nil), task.DependsOn...),
			Criteria:  blueprint.CompletionCriteria(task),
		})
	}
	if err := validateLegGraph(legs); err != nil {
		return nil, err
	}
	return legs, nil
}

func extractStrictTasks(content string) ([]blueprint.Task, bool, error) {
	start := strings.Index(content, tasksMarkerStart)
	if start < 0 {
		return nil, false, nil
	}
	rest := content[start+len(tasksMarkerStart):]
	end := strings.Index(rest, tasksMarkerEnd)
	if end < 0 {
		return nil, true, fmt.Errorf("unclosed blueprint tasks block")
	}
	raw := strings.TrimSpace(rest[:end])
	if raw == "" {
		return nil, true, nil
	}
	var tasks []blueprint.Task
	if err := json.Unmarshal([]byte(raw), &tasks); err != nil {
		return nil, true, fmt.Errorf("invalid blueprint tasks json: %w", err)
	}
	return tasks, true, nil
}

func validateLegGraph(legs []LegSpec) error {
	byID := make(map[string]LegSpec, len(legs))
	for _, leg := range legs {
		if _, exists := byID[leg.WorkID]; exists {
			return fmt.Errorf("duplicate blueprint task id %q", leg.WorkID)
		}
		byID[leg.WorkID] = leg
	}
	for _, leg := range legs {
		seen := make(map[string]bool, len(leg.DependsOn))
		for _, raw := range leg.DependsOn {
			dep := strings.TrimSpace(raw)
			switch {
			case dep == "" || dep != raw:
				return fmt.Errorf("blueprint task %q has invalid dependency %q", leg.WorkID, raw)
			case dep == leg.WorkID:
				return fmt.Errorf("blueprint task %q depends on itself", leg.WorkID)
			case seen[dep]:
				return fmt.Errorf("blueprint task %q repeats dependency %q", leg.WorkID, dep)
			case byID[dep].WorkID == "":
				return fmt.Errorf("blueprint task %q depends on unknown task %q", leg.WorkID, dep)
			}
			seen[dep] = true
		}
	}
	ancestors, err := computeAncestors(legs, byID)
	if err != nil {
		return err
	}
	for i := 0; i < len(legs); i++ {
		for j := i + 1; j < len(legs); j++ {
			a, b := legs[i], legs[j]
			if ancestors[a.WorkID][b.WorkID] || ancestors[b.WorkID][a.WorkID] {
				continue
			}
			if a.ScopeMode == workerrecords.ScopeWrite && b.ScopeMode == workerrecords.ScopeWrite && PathsOverlap(a.Files, b.Files) {
				return fmt.Errorf("unordered blueprint tasks %q and %q overlap on write paths", a.WorkID, b.WorkID)
			}
		}
	}
	return nil
}

func computeAncestors(legs []LegSpec, byID map[string]LegSpec) (map[string]map[string]bool, error) {
	visiting := make(map[string]bool, len(legs))
	memo := make(map[string]map[string]bool, len(legs))
	var visit func(string) (map[string]bool, error)
	visit = func(id string) (map[string]bool, error) {
		if visiting[id] {
			return nil, fmt.Errorf("blueprint task dependency cycle includes %q", id)
		}
		if got, ok := memo[id]; ok {
			return got, nil
		}
		visiting[id] = true
		set := make(map[string]bool)
		for _, dep := range byID[id].DependsOn {
			set[dep] = true
			trans, err := visit(dep)
			if err != nil {
				return nil, err
			}
			for up := range trans {
				set[up] = true
			}
		}
		visiting[id] = false
		memo[id] = set
		return set, nil
	}
	for _, leg := range legs {
		if _, err := visit(leg.WorkID); err != nil {
			return nil, err
		}
	}
	return memo, nil
}

// PathsOverlap reports whether two write-path scopes can touch the same file.
func PathsOverlap(a, b []string) bool {
	ca := cleanPaths(a)
	cb := cleanPaths(b)
	if len(ca) == 0 || len(cb) == 0 {
		return true
	}
	for _, pa := range ca {
		for _, pb := range cb {
			if singlePathOverlap(pa, pb) {
				return true
			}
		}
	}
	return false
}

func singlePathOverlap(a, b string) bool {
	if isUniversalGlob(a) || isUniversalGlob(b) || a == b {
		return true
	}
	baseA, globA := literalPrefix(a)
	baseB, globB := literalPrefix(b)
	if baseA == "" || baseB == "" {
		if matched, err := path.Match(a, b); err == nil && matched {
			return true
		}
		if matched, err := path.Match(b, a); err == nil && matched {
			return true
		}
		return baseA == "" && baseB == ""
	}
	if baseA == baseB {
		return true
	}
	if strings.HasPrefix(baseA, baseB+"/") {
		return true
	}
	if strings.HasPrefix(baseB, baseA+"/") {
		return true
	}
	if globA {
		if matched, err := path.Match(a, b); err == nil && matched {
			return true
		}
	}
	if globB {
		if matched, err := path.Match(b, a); err == nil && matched {
			return true
		}
	}
	return false
}

func isUniversalGlob(p string) bool {
	return p == "." || p == "*" || p == "**" || p == "**/*"
}

func literalPrefix(p string) (string, bool) {
	idx := strings.IndexAny(p, "*?[")
	if idx < 0 {
		return strings.TrimSuffix(p, "/"), false
	}
	prefix := strings.TrimSuffix(p[:idx], "/")
	return prefix, true
}

func cleanPaths(in []string) []string {
	var out []string
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		trimmed := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		trimmed = strings.TrimPrefix(trimmed, "./")
		if trimmed == "" {
			continue
		}
		cleaned := path.Clean(trimmed)
		if strings.Contains(trimmed, "**") {
			cleaned = trimmed
		}
		if !seen[cleaned] {
			seen[cleaned] = true
			out = append(out, cleaned)
		}
	}
	return out
}
