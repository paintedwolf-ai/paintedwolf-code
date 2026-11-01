package toolhost

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandWholeFileQueryEditsUseDeclaredCapacity(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	memory := observedProgramMemory(t, env)
	chunk := bytes.Repeat([]byte{' '}, 1<<20)
	body := "{\"x\":1}\n"
	for _, size := range []int{31, 32} {
		path := filepath.Join(env.Dir, "near.json")
		file, err := os.Create(path)
		testutil.FailErr(t, "os.Create failed", err)
		for range size - 1 {
			if _, err = file.Write(chunk); err != nil {
				testutil.FailErr(t, "file.Write failed", err)
			}
		}
		if _, err = file.Write(chunk[:len(chunk)-len(body)]); err != nil {
			testutil.FailErr(t, "file.Write failed", err)
		}
		if _, err = io.WriteString(file, body); err != nil {
			testutil.FailErr(t, "io.WriteString failed", err)
		}
		if err = file.Close(); err != nil {
			testutil.FailErr(t, "file.Close failed", err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() != int64(size)<<20 {
			t.Fatalf("fixture size %v %v", info, err)
		}
		out, err := env.Call(t, t.Context(), `jq-edit --path near.json --query '.x = 2'`)
		testutil.FailErr(t, "env.Call failed", err)
		result := decodePipeOutcome(t, out)
		if result.State != "completed" || result.Status != 0 {
			t.Fatalf("%d MiB query %+v refusal %+v", size, result, result.Refusal)
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != "{\"x\":2}\n" {
			t.Fatalf("query publication %q err %v", actual, err)
		}
		if held := memory().InUse(); held.Working != 0 || held.State > 64<<10 {
			t.Fatalf("query leases remain %+v", held)
		}
		t.Logf("exact %d MiB query admitted peak %+v", size, memory().Peak())
	}
	path := filepath.Join(env.Dir, "over.json")
	file, err := os.Create(path)
	testutil.FailErr(t, "os.Create failed", err)
	for range 32 {
		if _, err = file.Write(chunk); err != nil {
			testutil.FailErr(t, "file.Write failed", err)
		}
	}
	if _, err = file.Write([]byte{'0'}); err != nil {
		testutil.FailErr(t, "file.Write failed", err)
	}
	if err = file.Close(); err != nil {
		testutil.FailErr(t, "file.Close failed", err)
	}
	out, err := env.Call(t, t.Context(), `jq-edit --path over.json --query '.x = 2'`)
	testutil.FailErr(t, "env.Call failed", err)
	result := decodePipeOutcome(t, out)
	if result.Refusal == nil || result.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" || result.CompletedEffects != 0 {
		t.Fatalf("over-cap query %+v", result)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != (32<<20)+1 {
		t.Fatalf("over-cap source changed %v %v", info, err)
	}
}

func TestCommandQueryStreamsBeyondCapturedInputCapacity(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "guidance.LoadHintConfigStock failed", err)
	if err = guidance.ValidateHintConfig(hints); err != nil {
		testutil.FailErr(t, "guidance.ValidateHintConfig failed", err)
	}
	env.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	memory := observedProgramMemory(t, env)
	path := filepath.Join(env.Dir, "stream.json")
	file, err := os.Create(path)
	testutil.FailErr(t, "os.Create failed", err)
	chunk := bytes.Repeat([]byte{' '}, 1<<20)
	for range 63 {
		if _, err = file.Write(chunk); err != nil {
			testutil.FailErr(t, "file.Write failed", err)
		}
	}
	if _, err = file.Write(chunk[:len(chunk)-1]); err != nil {
		testutil.FailErr(t, "file.Write failed", err)
	}
	if _, err = file.Write([]byte{'0'}); err != nil {
		testutil.FailErr(t, "file.Write failed", err)
	}
	if err = file.Close(); err != nil {
		testutil.FailErr(t, "file.Close failed", err)
	}
	out, err := env.Call(t, t.Context(), `jq . stream.json`)
	testutil.FailErr(t, "env.Call failed", err)
	result := decodePipeOutcome(t, out)
	if result.State != "completed" || result.Status != 0 || result.Stdout != "0\n" {
		t.Fatalf("64 MiB query %+v refusal %+v memory=%+v", result, result.Refusal, memory().Peak())
	}
	if memory().Peak().State > 16<<20 {
		t.Fatalf("query retained input rather than positions %+v", memory().Peak())
	}
	if held := memory().InUse(); held.Working != 0 || held.State > 64<<10 {
		t.Fatalf("query leases remain %+v", held)
	}
	t.Logf("64 MiB small-value stream admitted peak %+v", memory().Peak())
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	testutil.FailErr(t, "os.OpenFile failed", err)
	if _, err = file.Write([]byte{' '}); err != nil {
		testutil.FailErr(t, "file.Write failed", err)
	}
	if err = file.Close(); err != nil {
		testutil.FailErr(t, "file.Close failed", err)
	}
	out, err = env.Call(t, t.Context(), `jq . stream.json`)
	testutil.FailErr(t, "env.Call failed", err)
	result = decodePipeOutcome(t, out)
	if result.State != "completed" || result.Status != 0 || result.Stdout != "0\n" || result.Refusal != nil {
		t.Fatalf("stream beyond captured-input capacity %+v", result)
	}
	if memory().Peak().State > 16<<20 {
		t.Fatalf("query retained the larger stream %+v", memory().Peak())
	}
	if held := memory().InUse(); held.Working != 0 || held.State > 64<<10 {
		t.Fatalf("larger query leases remain %+v", held)
	}
}
