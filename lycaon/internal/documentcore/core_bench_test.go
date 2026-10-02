package documentcore

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Typing in a source file: one character into the middle of 100 KB, with the
// undo capture and omitted text the editor's accepted edits use.
func BenchmarkKeystrokeIn100KB(b *testing.B) {
	engine := benchmarkCore(b)
	seedForBenchmark(b, engine, strings.Repeat("func example() { return value + other }\n", 2500))
	b.ResetTimer()
	for i := range b.N {
		_, err := engine.Call(context.Background(), Request{Action: "edit", Handle: 1, CaptureUndo: true, OmitText: true,
			Edits: []Edit{{Index: uint32(50_000 + i%1000), Insert: "x"}}})
		testutil.FailErr(b, "type a character", err)
	}
}

// Opening the largest document the host accepts, with its first checkpoint.
func BenchmarkOpen4MiB(b *testing.B) {
	engine := benchmarkCore(b)
	text := strings.Repeat("a", 4<<20)
	b.ResetTimer()
	for range b.N {
		seedForBenchmark(b, engine, text)
		_, err := engine.Call(context.Background(), Request{Action: "drop", Handle: 1})
		testutil.FailErr(b, "drop document", err)
	}
}

func benchmarkCore(b *testing.B) *Engine {
	b.Helper()
	engine, err := New(context.Background())
	testutil.FailErr(b, "start document core", err)
	b.Cleanup(func() { _ = engine.Close(context.Background()) })
	return engine
}

func seedForBenchmark(b *testing.B, engine *Engine, text string) {
	b.Helper()
	_, err := engine.Call(context.Background(), Request{Action: "open", Handle: 1, Client: 1})
	testutil.FailErr(b, "open document", err)
	_, err = engine.Call(context.Background(), Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: text}}, Checkpoint: true, OmitText: true})
	testutil.FailErr(b, "seed document", err)
}
