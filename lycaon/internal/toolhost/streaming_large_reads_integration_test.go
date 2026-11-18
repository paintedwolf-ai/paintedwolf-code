//go:build integration

package toolhost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A 100 MB input streams through pipelines, checksums and multi-file edits
// without the heap or the admitted state growing with it.
func TestCommandStreamingLargeReads(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	memory := observedProgramMemory(t, env)
	line := "old " + strings.Repeat("x", 995) + "\n"
	chunk := []byte(strings.Repeat(line, 999) + "ERROR " + strings.Repeat("y", 993) + "\n")
	if len(chunk) != 1_000_000 {
		t.Fatal(len(chunk))
	}
	sum := writeRepeatedFixture(t, filepath.Join(env.Dir, "large.log"), chunk, 100)
	for i := range 10 {
		writeRepeatedFixture(t, filepath.Join(env.Dir, fmt.Sprintf("part%d", i)), chunk, 10)
	}
	asset := make([]byte, 1_000_000)
	for i := range asset {
		asset[i] = byte(i)
	}
	if err := os.Mkdir(filepath.Join(env.Dir, "assets"), 0o700); err != nil {
		testutil.FailErr(t, "os.Mkdir failed", err)
	}
	writeRepeatedFixture(t, filepath.Join(env.Dir, "assets", "image.bin"), asset, 50)
	for _, test := range []struct {
		name, script, want string
		status             int
	}{
		{"pipeline", "cat large.log | grep ERROR | wc -l", "100", 0},
		{"checksum", "sha256sum large.log", sum + "  large.log", 0},
		{"head", "head -n 1 large.log", strings.TrimSuffix(line, "\n"), 0},
		{"binary tree", "grep -r missing-pattern assets", "", 1},
		{"multi file sed", "sed 's/old/new/g' part* | wc -c", "100000000", 0},
		{"awk", "awk '{ print toupper($0) }' large.log | wc -l", "100000", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			peak := measureCommandHeap(t, func() {
				out, err := env.Call(t, t.Context(), test.script)
				testutil.FailErr(t, "env.Call failed", err)
				result := decodePipeOutcome(t, out)
				if result.State != "completed" || result.Status != test.status || strings.TrimSpace(result.Stdout) != test.want {
					t.Fatalf("%s: %+v; refusal=%+v memory=%+v", test.script, result, result.Refusal, memory().Peak())
				}
			})
			// The existing model-screening stage admits 32 MiB and can overlap
			// two backing allocations; 8 MiB covers bounded record/runtime metadata.
			// This stays below a single gathered 100 MB input. State admission below
			// independently detects retaining complete operands.
			if peak > (2*32+8)<<20 {
				t.Fatalf("streaming heap grew %d bytes for bounded-record input", peak)
			}
			if memory().Peak().State > 4<<20 {
				t.Fatalf("stream retained operand data: %+v", memory().Peak())
			}
			if held := memory().InUse(); held.Working != 0 || held.State > 64<<10 {
				t.Fatalf("leases remain: %+v", held)
			}
			t.Logf("100 MB stream: sampled heap growth=%d, admitted peak=%+v", peak, memory().Peak())
		})
	}
}
