package command

import (
	"strconv"

	"github.com/lycaon/lycaon/internal/toolrejection"
)

// stateGrepStops puts the program's stopped grep inputs on the call's result.
// Singular facts describe the first stopped input; counts cover every search.
func stateGrepStops(e *programEvaluation) {
	out := e.callTC.Effects.Out
	searches := e.run.tc.Effects.GrepStops.Searches()
	if out == nil || len(searches) == 0 {
		return
	}
	first := searches[0].Stopped[0]
	var inputs []string
	searched, matched := 0, 0
	for _, search := range searches {
		searched += search.SearchedFiles
		matched += search.MatchedFiles
		for _, file := range search.Stopped {
			inputs = append(inputs, file.Path+":"+strconv.Itoa(file.Line))
		}
	}
	out.Facts = out.Facts.WithFeedback(toolrejection.GrepStoppedFilesCode, map[string]any{
		"grep_input_path":                first.Path,
		"grep_stop_line":                 first.Line,
		"grep_record_bytes":              first.RecordBytes,
		"grep_stop_resource":             first.Resource,
		"grep_stop_memory":               first.Resource == "evaluation_memory_bytes",
		"grep_lines_emitted_before_stop": first.LinesEmitted,
		"grep_pattern_literal":           searches[0].PatternLiteral,
		"grep_stopped_count":             len(inputs),
		"grep_stopped_inputs":            inputs,
		"grep_searched_files":            searched,
		"grep_matched_files":             matched,
	}, nil)
}
