package command_test

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkProgramEntrySpillQuota(t *testing.T, programs int) {
	t.Helper()
	f := newProgramFixture(t)
	for i := range programs {
		out, err := f.call(t, t.Context(), fmt.Sprintf("external -- /bin/echo run-%d | tee out.txt", i))
		if err != nil || !strings.Contains(out, `"state":"completed"`) {
			t.Fatalf("program %d: %v %s", i, err, out)
		}
	}
}

func checkProgramEntryLoopRetention(t *testing.T, items int) {
	t.Helper()
	f := newLoopFixture(t)
	data := strings.Repeat("item\n", items)
	testutil.FailErr(t, "write loop input", os.WriteFile(filepath.Join(f.Dir, "items.txt"), []byte(data), 0o600))
	for _, form := range []struct {
		program       string
		children      int
		receiptBodies int
	}{
		{`while read -r i; do basename "$i" >/dev/null; done < items.txt`, 2*items + 1, 2*items + 2},
		{`for i in $(external -- /bin/cat items.txt); do basename "$i" >/dev/null; done`, items + 1, items + 2},
	} {
		out, err := f.call(t, t.Context(), form.program)
		testutil.FailErr(t, "run large loop", err)
		result := decodeOutcome(t, out)
		if result.State != "completed" || result.Status != 0 || result.Refusal != nil || result.Stdout != "" || result.ExecutedChildren != form.children {
			var events, bytes int64
			testutil.FailErr(t, "read trace usage", f.Database.QueryRowContext(t.Context(), `SELECT trace_events, trace_bytes FROM invocation_programs WHERE id=?`, result.ProgramID).Scan(&events, &bytes))
			t.Fatalf("large loop state=%s status=%d refusal=%+v output=%d children=%d trace=%d/%d", result.State, result.Status, result.Refusal, len(result.Stdout), result.ExecutedChildren, events, bytes)
		}
		var receipts int
		testutil.FailErr(t, "count loop child receipts", f.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions WHERE program_id=?`, result.ProgramID).Scan(&receipts))
		if receipts != form.receiptBodies {
			t.Fatalf("loop child receipts=%d, want %d for %q", receipts, form.receiptBodies, form.program)
		}
		var inline, spilled int
		testutil.FailErr(t, "count durable loop input bodies", f.Database.QueryRowContext(t.Context(), `SELECT count(*) FILTER (WHERE inline_body IS NOT NULL),count(*) FILTER (WHERE inline_body IS NULL) FROM invocation_program_outputs WHERE program_id=? AND stream='arguments'`, result.ProgramID).Scan(&inline, &spilled))
		if inline < receipts || spilled != 0 {
			t.Fatalf("loop input retention: %d inline, %d blob-backed for %d children", inline, spilled, receipts)
		}
	}
	out, err := f.call(t, t.Context(), "echo hi")
	testutil.FailErr(t, "run later program", err)
	if result := decodeOutcome(t, out); result.State != "completed" || result.Stdout != "hi\n" {
		t.Fatalf("later program failed: %+v", result)
	}
}
