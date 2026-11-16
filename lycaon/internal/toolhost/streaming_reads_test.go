package toolhost

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func observedProgramMemory(t *testing.T, env *pipeSemanticsEnv) func() *toolcontract.ProgramMemory {
	owners := env.Runtime.Owners
	var memory *toolcontract.ProgramMemory
	env.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, actual *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		memory = actual
		bound, err := owners(tc, paths, actual, admission)
		if err != nil {
			return bound, err
		}
		install := bound.Builtins
		bound.Builtins = func(ports command.BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
			builtins, release, err := install(ports)
			for name, owner := range builtins {
				builtins[name] = observedDispatcher{t: t, owner: owner}
			}
			return builtins, release, err
		}
		return bound, nil
	}
	return func() *toolcontract.ProgramMemory { return memory }
}

// measureCommandHeap samples retained Go heap after collection during execution.
// Fixtures are already on disk before the baseline; this is not an RSS claim.
func measureCommandHeap(t *testing.T, run func()) (peak uint64) {
	t.Helper()
	runtime.GC()
	samples := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(samples)
	baseline := samples[0].Value.Uint64()
	stop, done := make(chan struct{}), make(chan uint64)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		peak := baseline
		for {
			runtime.GC()
			metrics.Read(samples)
			peak = max(peak, samples[0].Value.Uint64())
			select {
			case <-stop:
				done <- peak - baseline
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() { close(stop); peak = <-done }()
	run()
	return
}

func writeRepeatedFixture(t *testing.T, path string, chunk []byte, repetitions int) string {
	t.Helper()
	file, err := os.Create(path)
	testutil.FailErr(t, "os.Create failed", err)
	hash := sha256.New()
	writer := io.MultiWriter(file, hash)
	for range repetitions {
		if _, err := writer.Write(chunk); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		testutil.FailErr(t, "file.Close failed", err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func TestCommandBinaryRedirectsAndZeroRecords(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	data := []byte{'b', 0, 0xff, 0, 'a', 0}
	if err := os.WriteFile(filepath.Join(env.Dir, "binary"), data, 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	if _, err := zip.Write(data); err != nil {
		testutil.FailErr(t, "zip.Write failed", err)
	}
	if err := zip.Close(); err != nil {
		testutil.FailErr(t, "zip.Close failed", err)
	}
	if err := os.WriteFile(filepath.Join(env.Dir, "a.gz"), compressed.Bytes(), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	for _, test := range []struct {
		script, destination string
		want                []byte
	}{
		{"cat < binary > copy", "copy", data},
		{"sort -z < binary > sorted", "sorted", []byte{'a', 0, 'b', 0, 0xff, 0}},
		{"gunzip < a.gz > decoded", "decoded", data},
		{"tr -d '\\0' < binary > nonzero", "nonzero", []byte{'b', 0xff, 'a'}},
	} {
		out, err := env.Call(t, t.Context(), test.script)
		testutil.FailErr(t, "env.Call failed", err)
		if result := decodePipeOutcome(t, out); result.State != "completed" || result.Status != 0 {
			t.Fatalf("%s: %+v", test.script, result)
		}
		actual, err := os.ReadFile(filepath.Join(env.Dir, test.destination))
		if err != nil || !bytes.Equal(actual, test.want) {
			t.Fatalf("%s: %x != %x, %v", test.script, actual, test.want, err)
		}
	}
}

func TestCommandDiffLargeSparseChangesAndNearWholeFileLimit(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	memory := observedProgramMemory(t, env)
	var left, right strings.Builder
	for i := range 50_000 {
		fmt.Fprintf(&left, "line %05d\n", i)
		if i%500 == 250 {
			fmt.Fprintf(&right, "changed %05d\n", i)
		} else {
			fmt.Fprintf(&right, "line %05d\n", i)
		}
	}
	if err := os.WriteFile(filepath.Join(env.Dir, "left"), []byte(left.String()), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.WriteFile(filepath.Join(env.Dir, "right"), []byte(right.String()), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	out, err := env.Call(t, t.Context(), "diff -U 0 left right")
	testutil.FailErr(t, "env.Call failed", err)
	result := decodePipeOutcome(t, out)
	if result.State != "completed" || result.Status != 1 || strings.Count(result.Stdout, "@@ -") != 100 || strings.Count(result.Stdout, "-line ") != 100 || strings.Count(result.Stdout, "+changed ") != 100 {
		t.Fatalf("sparse diff: %+v", result)
	}
	writeRepeatedFixture(t, filepath.Join(env.Dir, "near-limit"), bytes.Repeat([]byte{'x'}, 1<<20), 31)
	out, err = env.Call(t, t.Context(), "diff -q near-limit left")
	testutil.FailErr(t, "env.Call failed", err)
	result = decodePipeOutcome(t, out)
	if result.State != "completed" || result.Status != 1 || result.Stdout != "Files near-limit and left differ\n" {
		t.Fatalf("31 MiB whole-file diff: %+v", result)
	}
	if held := memory().InUse(); held.Working != 0 || held.State > 64<<10 {
		t.Fatalf("leases remain: %+v", held)
	}
	t.Logf("31 MiB whole-file diff admitted peak=%+v", memory().Peak())
}

type observedDispatcher struct {
	t     *testing.T
	owner commandprogram.OperationDispatcher
}

func (d observedDispatcher) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	result, err := d.owner.Invoke(ctx, request, streams)
	if reject := toolrejection.AsToolReject(err); reject != nil {
		d.t.Logf("%s refusal: %s %+v", request.OperationID, reject.Code, reject.Data)
	}
	return result, err
}

func TestCommandLargeAppendStreamsOriginalPrefix(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	memory := observedProgramMemory(t, env)
	chunk := bytes.Repeat([]byte{'x'}, 1<<20)
	writeRepeatedFixture(t, filepath.Join(env.Dir, "append.log"), chunk, 40)
	out, err := env.Call(t, t.Context(), `printf tail >> append.log`)
	testutil.FailErr(t, "env.Call failed", err)
	result := decodePipeOutcome(t, out)
	if result.State != "completed" || result.Status != 0 {
		t.Fatalf("append: %+v refusal %+v", result, result.Refusal)
	}
	file, err := os.Open(filepath.Join(env.Dir, "append.log"))
	testutil.FailErr(t, "os.Open failed", err)
	defer file.Close()
	info, err := file.Stat()
	testutil.FailErr(t, "file.Stat failed", err)
	var tail [4]byte
	if _, err = file.ReadAt(tail[:], 40<<20); err != nil || string(tail[:]) != "tail" || info.Size() != (40<<20)+4 {
		t.Fatalf("appended size %d tail %q err %v", info.Size(), tail, err)
	}
	if memory().Peak().State > 4<<20 {
		t.Fatalf("append gathered old destination %+v", memory().Peak())
	}
	if held := memory().InUse(); held.Working != 0 || held.State > 64<<10 {
		t.Fatalf("append leases remain %+v", held)
	}
}
