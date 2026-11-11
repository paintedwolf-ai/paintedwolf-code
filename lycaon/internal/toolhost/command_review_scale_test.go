package toolhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// A wide xargs review completes in one call, and its receipt work grows with
// the batches xargs runs, never with the operands in them. The counts are
// deterministic proxies for cost, so the test holds on any runner.
func TestCommandWideReviewCompletesWithoutManualBatching(t *testing.T) {
	for _, count := range []int{100, 13000} {
		f := newPerfFixture(t, nil)
		name := strings.Repeat("a", 60) + ".go"
		if err := os.WriteFile(filepath.Join(f.Dir, name), []byte("hello\n"), 0600); err != nil {
			t.Fatalf("write source: %v", err)
		}
		if err := os.WriteFile(filepath.Join(f.Dir, "args.txt"), []byte(strings.Repeat(name+"\n", count)), 0600); err != nil {
			t.Fatalf("write arguments: %v", err)
		}
		before := f.writerRowChanges(t)
		out, err := f.Call(t, context.Background(), "xargs wc -l < args.txt")
		if err != nil {
			t.Fatalf("wide review: %v", err)
		}
		writes := f.writerRowChanges(t) - before
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("decode outcome: %v", err)
		}
		if result["state"] != "completed" {
			t.Fatalf("%d operands: %s", count, out)
		}
		var outcome command.ProgramOutcome
		testutil.FailErr(t, "decode program outcome", json.Unmarshal([]byte(out), &outcome))
		receipts := f.receiptCounts(t, outcome.ProgramID)
		// xargs is one child and each wc batch is another.
		batches := int64(outcome.ExecutedChildren - 1)
		if batches < 1 || batches > int64(count/5000+1) {
			t.Fatalf("%d operands ran %d wc batches", count, batches)
		}
		// Measured: 15 trace events and 23 row writes per program, plus 12 and
		// 24 per batch. The bounds leave room for small changes, not per-operand work.
		if receipts.traceEvents > 16+16*batches || writes > 30+30*batches || receipts.executionRows > 2+2*batches {
			t.Fatalf("%d operands: receipt work scaled past %d batches: trace_events=%d row_writes=%d execution_rows=%d",
				count, batches, receipts.traceEvents, writes, receipts.executionRows)
		}
	}
}
