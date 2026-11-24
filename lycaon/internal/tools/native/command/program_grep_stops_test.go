package command

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestStateGrepStopsSummarizesEverySearch(t *testing.T) {
	e := &programEvaluation{run: &programRun{}}
	e.callTC.Effects.Out = &tools.ToolInvocationOut{}
	stops := &tools.GrepStops{}
	e.run.tc.Effects.GrepStops = stops
	stateGrepStops(e)
	if e.callTC.Effects.Out.Facts.HasCode(toolrejection.GrepStoppedFilesCode) {
		t.Fatal("a program with no stops carried the feedback")
	}
	stops.Add(tools.GrepSearch{SearchedFiles: 3})
	stops.Add(tools.GrepSearch{Stopped: []tools.GrepStoppedFile{{Path: "a", Line: 4, RecordBytes: 9, Resource: "evaluation_memory_bytes", LinesEmitted: 2}}, SearchedFiles: 3, MatchedFiles: 1, PatternLiteral: true})
	stops.Add(tools.GrepSearch{Stopped: []tools.GrepStoppedFile{{Path: "b", Line: 1, Resource: "regex_work_steps"}}, SearchedFiles: 2, MatchedFiles: 2})
	stateGrepStops(e)
	details := e.callTC.Effects.Out.Facts.FeedbackFor(toolrejection.GrepStoppedFilesCode).Details
	inputs, _ := details["grep_stopped_inputs"].([]string)
	if details["grep_input_path"] != "a" || details["grep_stop_line"] != 4 || details["grep_record_bytes"] != int64(9) ||
		details["grep_stop_memory"] != true || details["grep_lines_emitted_before_stop"] != 2 || details["grep_pattern_literal"] != true ||
		details["grep_stopped_count"] != 2 || details["grep_searched_files"] != 5 || details["grep_matched_files"] != 3 ||
		!slices.Equal(inputs, []string{"a:4", "b:1"}) {
		t.Fatalf("details = %+v", details)
	}
}
