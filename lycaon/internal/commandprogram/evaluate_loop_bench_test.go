package commandprogram

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// BenchmarkWhileReadChild measures incremental input and one dispatched
// basename child per iteration, including parsing and evaluation.
func BenchmarkWhileReadChild(b *testing.B) {
	const items = 9999
	input := strings.Repeat("item\n", items)
	program := `while read -r i; do basename "$i" >/dev/null; done < items.txt`
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		f := newEvalFixture(b)
		f.ports.Input = fakeInput{"items.txt": input}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		b.StartTimer()
		outcome, err := f.run(b, context.Background(), program)
		b.StopTimer()
		runtime.ReadMemStats(&after)
		if err != nil || outcome.Status != 0 || len(f.log.calls) != items {
			b.Fatalf("loop outcome: status=%d children=%d err=%v", outcome.Status, len(f.log.calls), err)
		}
		if used := f.ports.Memory.InUse(); used.Working != 0 || used.State != 0 {
			b.Fatalf("program memory after settlement: %+v", used)
		}
		allocated := after.TotalAlloc - before.TotalAlloc
		if allocated > items*(32<<10) {
			b.Fatalf("loop allocation %d bytes for %d children", allocated, items)
		}
		b.ReportMetric(float64(allocated)/items, "alloc_bytes/child")
		b.StartTimer()
	}
}
