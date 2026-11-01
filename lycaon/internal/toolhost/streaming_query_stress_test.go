//go:build stress

package toolhost

import (
	"bytes"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestStressCommandQueryDeclaredInputBoundary(t *testing.T) {
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
	if result.Refusal == nil || result.Refusal.Code != "JQ_INPUT_TOO_LARGE" {
		t.Fatalf("over-input query %+v", result)
	}
}
