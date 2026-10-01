package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
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
