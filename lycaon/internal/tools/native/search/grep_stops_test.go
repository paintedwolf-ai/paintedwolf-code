package search

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func stoppingHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, map[string]string{
		"a.txt": "x1y\n",
		"b.txt": "x2y\n" + "x" + strings.Repeat("7", 200000) + "\nx3y\n",
		"c.txt": "x4y\n",
	})
	h.owners.regex.WorkSteps = 20000
	h.tc.Effects.GrepStops = &tools.GrepStops{}
	return h
}

func TestGrepMultiFileContinuesPastStoppedRecord(t *testing.T) {
	h := stoppingHarness(t)
	r := h.exec("", "grep", "-n", "x[0-9]*y", "a.txt", "b.txt", "c.txt")
	r.expect(t, "multi-file stop",
		"a.txt:1:x1y\nb.txt:1:x2y\nc.txt:1:x4y\n",
		"grep: b.txt: line 2 exceeds the search budget (regex_work_steps)\n", 2)
	searches := h.tc.Effects.GrepStops.Searches()
	if len(searches) != 1 {
		t.Fatalf("searches = %+v", searches)
	}
	got := searches[0]
	want := tools.GrepStoppedFile{Path: "b.txt", Line: 2, RecordBytes: 200001, Resource: "regex_work_steps", LinesEmitted: 1}
	if len(got.Stopped) != 1 || got.Stopped[0] != want || got.SearchedFiles != 3 || got.MatchedFiles != 3 || got.PatternLiteral {
		t.Fatalf("search = %+v", got)
	}
}

func TestGrepRecursiveStopReportsEveryInput(t *testing.T) {
	h := stoppingHarness(t)
	r := h.exec("", "grep", "-rc", "x[0-9]*y", ".")
	if r.err != nil || r.result.Status != 2 || !strings.Contains(r.stderr, "b.txt: line 2 exceeds the search budget") {
		t.Fatalf("recursive stop: %+v err %v stderr %q", r.result, r.err, r.stderr)
	}
	for _, line := range []string{"./a.txt:1\n", "./c.txt:1\n"} {
		if !strings.Contains(r.stdout, line) {
			t.Fatalf("stdout %q lacks %q", r.stdout, line)
		}
	}
	if strings.Contains(r.stdout, "b.txt") {
		t.Fatalf("stopped input printed a partial count: %q", r.stdout)
	}
}

func TestGrepSingleInputStopStillRefuses(t *testing.T) {
	h := stoppingHarness(t)
	r := h.exec("", "grep", "x[0-9]*y", "b.txt")
	r.refused(t, "single input", "GREP_MATCH_BUDGET")
	data := toolrejection.AsToolReject(r.err).Data
	if data["grep_input_path"] != "b.txt" || data["grep_stop_line"] != 2 || data["grep_record_bytes"] != int64(200001) ||
		data["grep_stop_resource"] != "regex_work_steps" || data["grep_stop_memory"] != false || data["grep_pattern_literal"] != false {
		t.Fatalf("refusal data = %+v", data)
	}
	if searches := h.tc.Effects.GrepStops.Searches(); len(searches) != 0 {
		t.Fatalf("single input recorded a stop: %+v", searches)
	}
}
