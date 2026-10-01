package loopguard

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/testutil"
)

// One tool rejected under one Code while the args keep changing: the arc the
// identical-args counters cannot see, since every rewording lands in a new
// bucket and any success clears the consecutive run.
func TestPerCodeTotalSurvivesRewordingAndSuccess(t *testing.T) {
	t.Parallel()
	g := NewMemoryDoomLoopGuard()
	ctx := context.Background()
	const sess = "s1"

	reworded := []string{
		`list | grep -E "^.*name@"`,
		`list | grep -E "name@1.2.3"`,
		`list | grep "name@1.2.3"`,
	}
	for i, command := range reworded {
		args := map[string]any{"command": command}
		testutil.FailErr(t, "RecordAttempt", g.RecordAttempt(ctx, sess, uuid.NewString(), "command", args, "COMMAND_NOT_ARGV", false))
		if got := g.CodeRejectResponses(sess, "command", "COMMAND_NOT_ARGV"); got != i+1 {
			t.Fatalf("after %d rejections total=%d — rewording the args must not reset the Code's count", i+1, got)
		}
	}

	if got := g.CodeRejectResponses(sess, "command", "COMMAND_NOT_ARGV"); got < DoomLoopMaxCodeRepeats {
		t.Fatalf("total=%d never reaches the escalation threshold %d", got, DoomLoopMaxCodeRepeats)
	}

	// The identical-args counter sees three buckets of one on the same arc.
	for _, command := range reworded {
		allowed, count, repeatedCode, err := g.Check(ctx, sess, uuid.NewString(), "command", map[string]any{"command": command})
		testutil.FailErr(t, "Check", err)
		if !allowed || repeatedCode != "" {
			t.Fatalf("identical-args guard blocked a reworded call (count=%d code=%q)", count, repeatedCode)
		}
	}
}

// Counting is per (tool, Code), so an unrelated rule's rejection does not accrue.
func TestPerCodeTotalIsScopedToToolAndCode(t *testing.T) {
	t.Parallel()
	g := NewMemoryDoomLoopGuard()
	ctx := context.Background()
	const sess = "s1"
	args := map[string]any{"command": "x"}

	testutil.FailErr(t, "RecordAttempt pipeline", g.RecordAttempt(ctx, sess, uuid.NewString(), "command", args, "COMMAND_NOT_ARGV", false))
	testutil.FailErr(t, "RecordAttempt missing command", g.RecordAttempt(ctx, sess, uuid.NewString(), "command", args, "COMMAND_NOT_FOUND", false))
	testutil.FailErr(t, "RecordAttempt verify", g.RecordAttempt(ctx, sess, uuid.NewString(), "verify", args, "COMMAND_NOT_ARGV", false))

	for _, tc := range []struct {
		tool, code string
		want       int
	}{
		{"command", "COMMAND_NOT_ARGV", 1},
		{"command", "COMMAND_NOT_FOUND", 1},
		{"verify", "COMMAND_NOT_ARGV", 1},
		{"command", "GREP_REGEX_INVALID", 0},
	} {
		if got := g.CodeRejectResponses(sess, tc.tool, tc.code); got != tc.want {
			t.Errorf("CodeRejectResponses(%s, %s)=%d want %d", tc.tool, tc.code, got, tc.want)
		}
	}
}

// Sessions do not pool rejections, and a rejection with no Code accrues nowhere.
func TestPerCodeTotalIsPerSessionAndIgnoresCodelessRejections(t *testing.T) {
	t.Parallel()
	g := NewMemoryDoomLoopGuard()
	ctx := context.Background()
	args := map[string]any{"command": "x"}

	testutil.FailErr(t, "RecordAttempt coded", g.RecordAttempt(ctx, "s1", uuid.NewString(), "command", args, "COMMAND_NOT_ARGV", false))
	testutil.FailErr(t, "RecordAttempt codeless", g.RecordAttempt(ctx, "s1", uuid.NewString(), "command", args, "", false))

	if got := g.CodeRejectResponses("s1", "command", "COMMAND_NOT_ARGV"); got != 1 {
		t.Errorf("s1 total=%d want 1 — a codeless rejection must not accrue to a Code", got)
	}
	if got := g.CodeRejectResponses("s2", "command", "COMMAND_NOT_ARGV"); got != 0 {
		t.Errorf("s2 total=%d want 0 — one session's rejections are not another's", got)
	}
}
