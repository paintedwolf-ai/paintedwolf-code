package loopguard

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTerminalProgressRewordingStillRepeats(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	for i := range DoomLoopMaxAttempts {
		args := map[string]any{"content": fmt.Sprintf("## Progress\n- [x] Finished %d", i)}
		testutil.FailErr(t, "record completed checklist", g.RecordAttempt(t.Context(), "session", uuid.NewString(), "update_progress", args, nil, true))
	}
	args := map[string]any{"content": "## Progress\n- [x] Reworded completion"}
	allowed, count, _, err := g.Check(t.Context(), "session", uuid.NewString(), "update_progress", args, false, nil)
	testutil.FailErr(t, "check repeated completion", err)
	if allowed || count != DoomLoopMaxAttempts {
		t.Fatalf("allowed=%v count=%d", allowed, count)
	}
	testutil.FailErr(t, "record real work", g.RecordAttempt(t.Context(), "session", uuid.NewString(), "write", map[string]any{"path": "main.go"}, nil, true))
	allowed, _, _, err = g.Check(t.Context(), "session", uuid.NewString(), "update_progress", args, false, nil)
	testutil.FailErr(t, "check completion after work", err)
	if !allowed {
		t.Fatal("real intervening work must permit another completed checklist")
	}
}

func TestProgressFingerprintKeepsPendingContent(t *testing.T) {
	for _, content := range []string{"", "## Progress\n- [ ] Pending", "## Progress\n- [x] Done\n- [ ] Next"} {
		got := doomLoopUpdateProgressArgs(map[string]any{"content": content})
		if got["content"] != content {
			t.Fatalf("pending or empty content lost: %+v", got)
		}
	}
	if got := doomLoopUpdateProgressArgs(nil); len(got) != 0 {
		t.Fatalf("nil args = %+v", got)
	}
}
