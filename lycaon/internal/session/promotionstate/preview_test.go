package promotionstate

import (
	"github.com/lycaon/lycaon/pkg/api"
	"reflect"
	"testing"
)

func TestPreviewRetainsConflictsAndClearsCompletedJobs(t *testing.T) {
	s := New()
	rows := NormalizePromotePathStatusRows([]api.WorkerPromotePathStatus{{Path: "src/../a.go", Status: api.WorkerPromotePathOutcomeConflict}, {Path: "a.go"}, {Path: "../escape"}, {Path: "b.go"}})
	if len(rows) != 2 || rows[0].Path != "a.go" {
		t.Fatalf("normalized rows = %+v", rows)
	}
	s.RecordPromotePathStatus("session", "job", rows)
	s.RecordOverlayPreviewSummary("session", "job", &api.WorkerMergeResult{CleanPaths: []string{"b.go"}, PathStatus: rows, ConflictDigest: []api.WorkerPromoteConflictDigest{{Path: "a.go"}}})
	_, _, _, clean, conflicts, ok := s.OverlayPreviewSnapshot("session", "job")
	if !ok || !reflect.DeepEqual(clean, []string{"b.go"}) || !reflect.DeepEqual(conflicts, []string{"a.go"}) {
		t.Fatalf("preview clean=%v conflicts=%v available=%v", clean, conflicts, ok)
	}
	clean[0] = "changed"
	board := s.PromotePathBoardLines("session")
	if len(board) != 1 || board[0].WorkerID != "job" || len(board[0].Paths) != 2 {
		t.Fatalf("board = %+v", board)
	}
	board[0].Paths[0].Path = "changed"
	_, _, _, clean, _, _ = s.OverlayPreviewSnapshot("session", "job")
	if clean[0] != "b.go" || s.PromotePathBoardLines("session")[0].Paths[0].Path != "a.go" {
		t.Fatal("caller mutated retained preview")
	}
	s.ClearPromotePathStatus("session", "job")
	if len(s.PromotePathBoardLines("session")) != 0 {
		t.Fatal("completed job retained on board")
	}
	_, _, _, _, _, ok = s.OverlayPreviewSnapshot("session", "job")
	if ok {
		t.Fatal("completed preview retained")
	}
	s.SetMergeReconcilePaths("session", []string{"./a.go", "a.go", "../escape"})
	if !s.Allowed("session", "a.go") || s.Allowed("session", "../escape") || s.Allowed("other", "a.go") {
		t.Fatal("reconcile allowance escaped session/path")
	}
	s.Forget("session")
	if s.Allowed("session", "a.go") {
		t.Fatal("forgotten session retained reconcile allowance")
	}
}
