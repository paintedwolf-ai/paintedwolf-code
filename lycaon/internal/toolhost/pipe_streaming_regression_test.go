package toolhost

import (
	"bytes"
	"compress/gzip"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPipeSemanticsIncrementalNativeConsumers(t *testing.T) {
	for _, tc := range []struct{ name, program, output string }{
		{"xargs", "yes hello | xargs -n1 echo | head -1", "hello\n"},
		{"tee", "yes hello | tee | head -1", "hello\n"},
		{"yq", `yes '{"value":1}' | yq -p json -o json -I0 . | head -1`, "{\"value\":1}\n"},
		{"sed", "yes hello | sed -n '1p;1q'", "hello\n"},
		{"awk", "yes hello | awk '{ print; exit }'", "hello\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newPipeSemanticsEnv(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			out, err := env.Call(t, ctx, tc.program)
			testutil.FailErr(t, "run incremental "+tc.name, err)
			result := decodePipeOutcome(t, out)
			if result.Status != 0 || result.State != "completed" || result.Stdout != tc.output {
				t.Fatalf("%s: status=%d state=%s stdout=%q stderr=%q", tc.name, result.Status, result.State, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestPipeSemanticsSpilledSortIntoEarlyConsumer(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// Per-record bookkeeping alone exceeds the catalog's 8 MiB sort buffer.
	out, err := env.Call(t, ctx, "seq 200000 -1 1 | sort -n | head -3")
	testutil.FailErr(t, "spilled sort into head", err)
	result := decodePipeOutcome(t, out)
	if result.Status != 0 || result.State != "completed" || result.Stdout != "1\n2\n3\n" {
		t.Fatalf("spilled sort result=%+v", result)
	}
}

func TestPipeSemanticsPrintfPaddingIntoEarlyConsumer(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, item := range []struct{ format, want string }{{"%100000000s", " "}, {"%100000000d", " "}, {"%0100000000d", "0"}, {"%#0100000000x", "0"}, {"%100000000.2f", " "}} {
		out, err := env.Call(t, ctx, "printf '"+item.format+"' 1 | head -c1")
		testutil.FailErr(t, "streamed printf padding", err)
		outcome := decodePipeOutcome(t, out)
		if outcome.Status != 0 || outcome.Stdout != item.want {
			t.Fatalf("padding %s: %+v", item.format, outcome)
		}
	}
}

func TestPipeSemanticsGzipBeforeUpstreamEOF(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	payload := make([]byte, 128<<10)
	_, err := rand.New(rand.NewSource(1)).Read(payload)
	testutil.FailErr(t, "seed deterministic payload", err)
	copy(payload, "ready\n")
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	_, err = writer.Write(payload)
	testutil.FailErr(t, "compress payload", err)
	testutil.FailErr(t, "flush gzip prefix", writer.Flush())
	prefix := encoded.Len()
	testutil.FailErr(t, "close gzip", writer.Close())
	testutil.FailErr(t, "write prefix", os.WriteFile(filepath.Join(env.Dir, "prefix.gz"), encoded.Bytes()[:prefix], 0o600))
	testutil.FailErr(t, "write suffix", os.WriteFile(filepath.Join(env.Dir, "suffix.gz"), encoded.Bytes()[prefix:], 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	out, err := env.Call(t, ctx, `external -- /bin/sh -c 'cat prefix.gz; while [ ! -f downstream.ready ]; do sleep .01; done; cat suffix.gz' | zcat | external -- /bin/sh -c 'IFS= read -r line; printf "%s\n" "$line"; : > downstream.ready'`)
	testutil.FailErr(t, "gzip before upstream EOF", err)
	result := decodePipeOutcome(t, out)
	if result.Status != 0 || result.Stdout != "ready\n" {
		t.Fatalf("gzip progress: %+v", result)
	}
}
