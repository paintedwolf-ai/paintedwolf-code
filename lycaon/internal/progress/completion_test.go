package progress_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
)

func TestAllTerminal(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"no checklist", "# Goal\njust prose", false},
		{"has pending", "- [x] a\n- [ ] b", false},
		{"all done", "- [x] a\n- [x] b", true},
		{"single done", "- [x] only", true},
	}
	for _, tc := range cases {
		if got := progress.AllTerminal(tc.content); got != tc.want {
			t.Fatalf("%s: AllTerminal = %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestReserveCompletion_edgeAndRearm(t *testing.T) {
	sess := uniqueProgressKey("session-rearm")

	if _, ok := progress.ReserveCompletion(sess, false); ok {
		t.Fatal("pending write should not emit")
	}

	emit, ok := progress.ReserveCompletion(sess, true)
	if !ok || emit.Seq != 1 {
		t.Fatalf("first terminal edge: ok=%v seq=%d want true,1", ok, emit.Seq)
	}
	emit.Commit()

	if _, ok := progress.ReserveCompletion(sess, true); ok {
		t.Fatal("repeated terminal state should not re-emit")
	}

	if _, ok := progress.ReserveCompletion(sess, false); ok {
		t.Fatal("re-arm write should not emit")
	}

	emit, ok = progress.ReserveCompletion(sess, true)
	if !ok || emit.Seq != 2 {
		t.Fatalf("second terminal edge: ok=%v seq=%d want true,2", ok, emit.Seq)
	}
	emit.Commit()
}

func TestReserveCompletion_abortAllowsRetry(t *testing.T) {
	sess := uniqueProgressKey("session-abort")

	first, ok := progress.ReserveCompletion(sess, true)
	if !ok || first.Seq != 1 {
		t.Fatalf("first reserve: ok=%v seq=%d want true,1", ok, first.Seq)
	}
	if _, ok := progress.ReserveCompletion(sess, true); ok {
		t.Fatal("second reserve while held should fail")
	}
	first.Abort()

	retry, ok := progress.ReserveCompletion(sess, true)
	if !ok || retry.Seq != 1 {
		t.Fatalf("retry after abort: ok=%v seq=%d want true,1", ok, retry.Seq)
	}
	retry.Commit()
}

func TestReserveCompletion_blankSession(t *testing.T) {
	if emit, ok := progress.ReserveCompletion("  ", true); ok || emit.Seq != 0 {
		t.Fatalf("blank session: ok=%v seq=%d want false,0", ok, emit.Seq)
	}
}
