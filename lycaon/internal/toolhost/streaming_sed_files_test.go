package toolhost

import (
	"bytes"
	"os"
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

func TestCommandSedPreparedAdmissionAndFileScriptPins(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	memory := observedProgramMemory(t, env)
	testutil.FailErr(t, "write input", os.WriteFile(filepath.Join(env.Dir, "in.txt"), []byte("alpha\nbeta\n"), 0o644))
	testutil.FailErr(t, "write script", os.WriteFile(filepath.Join(env.Dir, "edit.sed"), []byte("s/alpha/omega/\n"), 0o644))
	testutil.FailErr(t, "write w script", os.WriteFile(filepath.Join(env.Dir, "write.sed"), []byte("w out.txt\n"), 0o644))

	out, err := env.Call(t, t.Context(), `sed -f edit.sed in.txt && sed -i .bak 's/beta/delta/' in.txt`)
	testutil.FailErr(t, "run sed -f and BSD -i", err)
	result := decodePipeOutcome(t, out)
	if result.Status != 0 || result.State != "completed" || result.Refusal != nil || result.Stdout != "omega\nbeta\n" || result.CompletedEffects != 1 || memory().InUse().Working != 0 {
		t.Fatalf("sed -f and BSD -i outcome: %+v usage=%+v", result, memory().InUse())
	}
	edited, err := os.ReadFile(filepath.Join(env.Dir, "in.txt"))
	testutil.FailErr(t, "read edited in.txt", err)
	backup, err := os.ReadFile(filepath.Join(env.Dir, "in.txt.bak"))
	testutil.FailErr(t, "read in.txt.bak", err)
	if string(edited) != "alpha\ndelta\n" || string(backup) != "alpha\nbeta\n" {
		t.Fatalf("edited=%q backup=%q", edited, backup)
	}

	var scriptPinCount int
	testutil.FailErr(t, "query script pin", env.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_revision_pins WHERE path = 'edit.sed' AND role = 'source'`).Scan(&scriptPinCount))
	if scriptPinCount != 1 {
		t.Fatalf("expected 1 revision pin for edit.sed after child creation, got %d", scriptPinCount)
	}

	for _, tc := range []struct {
		program, wantCode string
	}{
		{`sed 'e date' in.txt`, "COMMAND_OPTION_UNSUPPORTED"},
		{`sed 's/a/b/M' in.txt`, "COMMAND_OPTION_UNSUPPORTED"},
	} {
		out, err := env.Call(t, t.Context(), tc.program)
		testutil.FailErr(t, tc.program, err)
		refused := decodePipeOutcome(t, out)
		if refused.Refusal == nil || refused.Refusal.Code != tc.wantCode || refused.NativeGaps != 0 || memory().InUse().Working != 0 {
			t.Fatalf("%s: outcome=%+v refusal=%+v usage=%+v", tc.program, refused, refused.Refusal, memory().InUse())
		}
	}
	out, err = env.Call(t, t.Context(), `sed -f write.sed in.txt`)
	testutil.FailErr(t, "run sed -f with reviewed write", err)
	written := decodePipeOutcome(t, out)
	if written.Status != 0 || written.State != "completed" || written.Refusal != nil || written.CompletedEffects != 1 {
		t.Fatalf("sed -f reviewed write outcome: %+v", written)
	}
	data, err := os.ReadFile(filepath.Join(env.Dir, "out.txt"))
	testutil.FailErr(t, "read sed -f output", err)
	if string(data) != "alpha\ndelta\n" {
		t.Fatalf("sed -f output=%q", data)
	}
}
