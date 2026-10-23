package toolhost

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandSedScriptFilesStreamLargePinnedBytes(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	memory := observedProgramMemory(t, env)
	chunk := bytes.Repeat([]byte{'a'}, 64<<10)
	chunk[13], chunk[17], chunk[len(chunk)-1] = 0, 0xff, '\n'
	want := writeRepeatedFixture(t, filepath.Join(env.Dir, "asset.bin"), chunk, 1600)
	for _, command := range []string{
		`printf 'trigger\n' | sed -n 'r asset.bin' | sha256sum`,
		`seq 1600 | sed -n 'R asset.bin' | sha256sum`,
	} {
		peak := measureCommandHeap(t, func() {
			out, err := env.Call(t, t.Context(), command)
			testutil.FailErr(t, "stream binary script file", err)
			result := decodePipeOutcome(t, out)
			if result.Status != 0 || result.State != "completed" || result.Refusal != nil || result.Stdout != want+"  -\n" || memory().InUse().Working != 0 || memory().InUse().State > 64<<10 {
				t.Fatalf("script file bytes or cleanup: %+v usage=%+v", result, memory().InUse())
			}
		})
		// Two screening backings plus8MiB for bounded runtime/record data
		// remain below even one gathered100MiB asset.
		if peak > (2*32+8)<<20 {
			t.Fatalf("script file retained %d heap bytes", peak)
		}

		// Screening retains a fixed32MiB allowance. File input adds only
		// block proofs and record/copy buffers, rather than the100MiB asset.
		if memory().Peak().Working > 40<<20 || memory().Peak().State > 4<<20 {
			t.Fatalf("script file gathered input: %+v", memory().Peak())
		}
		t.Logf("script-file sampled heap=%d admitted peak %+v", peak, memory().Peak())
	}
	out, err := env.Call(t, t.Context(), `seq 1600 | sed -n 'R asset.bin' | head -c 1 | wc -c`)
	testutil.FailErr(t, "close downstream script-file consumer", err)
	result := decodePipeOutcome(t, out)
	if result.Status != 0 || result.State != "completed" || result.Refusal != nil || result.Stdout != "1\n" || memory().InUse().Working != 0 || memory().InUse().State > 64<<10 {
		t.Fatalf("closed script-file consumer lost completion or cleanup: %+v usage=%+v", result, memory().InUse())
	}

}
