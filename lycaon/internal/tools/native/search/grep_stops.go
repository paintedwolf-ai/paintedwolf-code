package search

import (
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/tools"
)

// recordStop reports whether a budget stop belongs to one record's search, so
// a multi-file grep can end that input and continue with the next.
func recordStop(resource string) bool {
	return resource == "regex_work_steps" || resource == "evaluation_memory_bytes"
}

// grepRecordStop is a single-input search stopped on one record's budget.
type grepRecordStop struct {
	budget  *textsearch.BudgetError
	path    string
	literal bool
}

func (e *grepRecordStop) Error() string { return e.budget.Error() }
func (e *grepRecordStop) Unwrap() error { return e.budget }

// facts are the refusal's record facts.
func (e *grepRecordStop) facts() map[string]any {
	return map[string]any{
		"grep_input_path": e.path, "grep_stop_line": e.budget.Line, "grep_record_bytes": e.budget.RecordBytes,
		"grep_stop_resource": e.budget.Resource, "grep_stop_memory": e.budget.Resource == "evaluation_memory_bytes",
		"grep_pattern_literal": e.literal,
	}
}

// stopInput ends one input of a multi-file search at a record budget. Lines it
// printed before the stop stay in the output.
func (r *grepRun) stopInput(label string, budget *textsearch.BudgetError, selected, emitted int) error {
	r.inv.warn(false, "%s: line %d exceeds the search budget (%s)", label, budget.Line, budget.Resource)
	r.inv.status = max(r.inv.status, 2)
	r.stops = append(r.stops, tools.GrepStoppedFile{
		Path: label, Line: budget.Line, RecordBytes: budget.RecordBytes, Resource: budget.Resource, LinesEmitted: emitted,
	})
	r.searched++
	if selected > 0 {
		r.matched++
	}
	return nil
}

// reportStops records this search's stopped inputs on the program run.
func (r *grepRun) reportStops() {
	if len(r.stops) == 0 {
		return
	}
	r.inv.tc.Effects.GrepStops.Add(tools.GrepSearch{
		Stopped: r.stops, SearchedFiles: r.searched, MatchedFiles: r.matched,
		PatternLiteral: r.matcher != nil && r.matcher.Literal(),
	})
}
