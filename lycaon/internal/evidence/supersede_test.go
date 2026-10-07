package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSupersedesPathHandle_readAndMutationOnly(t *testing.T) {
	read := evidence.Record{Kind: "read", Path: "a.go"}
	grep := evidence.Record{Kind: "grep", Path: "a.go"}
	write := evidence.Record{Kind: "write", Path: "a.go"}
	find := evidence.Record{Kind: "find", Path: "a.go"}

	if !evidence.SupersedesPathHandle(read, grep) {
		t.Fatal("later read must supersede prior grep")
	}
	if !evidence.SupersedesPathHandle(read, read) {
		t.Fatal("later read must supersede prior read")
	}
	if evidence.SupersedesPathHandle(grep, read) {
		t.Fatal("grep must not supersede a content read")
	}
	if evidence.SupersedesPathHandle(find, read) {
		t.Fatal("find must not supersede a content read")
	}
	if evidence.SupersedesPathHandle(grep, grep) {
		t.Fatal("grep must not whole-record-retire another grep")
	}
	if !evidence.SupersedesPathHandle(write, read) {
		t.Fatal("mutation must supersede prior read")
	}
	if evidence.SupersedesPathHandle(read, write) {
		t.Fatal("read must not supersede a mutation handle")
	}
}

func TestFileObservationCannotSupersedeEventReceipts(t *testing.T) {
	for _, kind := range []string{"command", "git", "git_receipt", "scan", "http_response"} {
		for _, replacement := range []string{"read", "write", "delete"} {
			if evidence.SupersedesPathHandle(evidence.Record{Kind: replacement, Path: "a.go"}, evidence.Record{Kind: kind, Path: "a.go"}) {
				t.Fatalf("%s superseded %s event receipt", replacement, kind)
			}
		}
	}
}

func TestPartialReadDoesNotSupersedeEarlierRange(t *testing.T) {
	oldRead := evidence.Record{
		Kind:       "read",
		Path:       "feed.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 130}},
	}
	newRead := evidence.Record{
		Kind:       "read",
		Path:       "feed.go",
		LineRanges: []evidence.LineRange{{Start: 131, End: 157}},
	}
	if evidence.SupersedesPathHandle(newRead, oldRead) {
		t.Fatal("partial non-overlapping read must not supersede earlier range")
	}
}

func TestFullCoverReadSupersedes(t *testing.T) {
	oldRead := evidence.Record{
		Kind:       "read",
		Path:       "feed.go",
		LineRanges: []evidence.LineRange{{Start: 10, End: 50}},
	}
	newRead := evidence.Record{
		Kind:       "read",
		Path:       "feed.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 100}},
	}
	if !evidence.SupersedesPathHandle(newRead, oldRead) {
		t.Fatal("encompassing read must supersede earlier subrange")
	}
}

func TestReadNeverSupersedesMultiPathGrep(t *testing.T) {
	root := t.TempDir()
	grepJSON := `{"matches":[{"path":"src/a.go","line":10,"content":"x"},{"path":"src/b.go","line":20,"content":"y"}]}`
	ev := ledgertest.BuildFromMessages(root, []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "grep", ID: "c1", Args: map[string]any{"path": "src", "pattern": "x"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: grepJSON}},
	})
	grepRec, ok := evidence.ResolveHandle(ev, "grep#1")
	if !ok {
		t.Fatal("missing grep record")
	}
	readRec := evidence.Record{
		Kind:       "read",
		Path:       "src/a.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 100}},
	}
	if evidence.SupersedesPathHandle(readRec, grepRec) {
		t.Fatal("single-file read must never supersede multi-path grep")
	}
}
