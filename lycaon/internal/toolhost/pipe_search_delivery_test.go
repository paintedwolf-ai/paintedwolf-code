package toolhost

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// countedInput counts the reads that returned bytes. A pipe hands its reader
// one producer write at a time, so the count bounds the producer's writes.
type countedInput struct {
	io.ReadCloser
	deliveries *int
}

func (c countedInput) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 {
		*c.deliveries++
	}
	return n, err
}

// countPipeDeliveries counts what the named consumer reads from its pipe.
func countPipeDeliveries(t *testing.T, env *pipeSemanticsEnv, consumer string) *int {
	t.Helper()
	deliveries := new(int)
	owners := env.Runtime.Owners
	env.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		bound, err := owners(tc, paths, memory, admission)
		if err != nil {
			return bound, err
		}
		native := bound.Natives[consumer]
		if native.Prepare == nil {
			t.Fatalf("%s has no native owner to observe", consumer)
		}
		prepare := native.Prepare
		native.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := prepare(ctx, paths, dispatch, request)
			if err != nil {
				return prepared, err
			}
			invoke := prepared.Invoke
			prepared.Invoke = func(ctx context.Context, admitted tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
				streams.Input = countedInput{ReadCloser: streams.Input, deliveries: deliveries}
				return invoke(ctx, admitted, streams)
			}
			return prepared, nil
		}
		bound.Natives[consumer] = native
		return bound, nil
	}
	return deliveries
}

// A pipe keeps no source mappings, so search batches its records for one:
// the consumer sees far fewer deliveries than records.
func TestSearchBatchesRecordsIntoAPipe(t *testing.T) {
	const records = 20_000
	env := newPipeSemanticsEnv(t)
	var lines strings.Builder
	for i := range records {
		fmt.Fprintf(&lines, "needle %05d\n", i)
	}
	testutil.FailErr(t, "write lines", os.WriteFile(filepath.Join(env.Dir, "lines.txt"), []byte(lines.String()), 0o600))
	tree := filepath.Join(env.Dir, "tree")
	testutil.FailErr(t, "create tree", os.Mkdir(tree, 0o700))
	for i := range records {
		testutil.FailErr(t, "create entry", os.WriteFile(filepath.Join(tree, fmt.Sprintf("f%05d", i)), nil, 0o600))
	}
	deliveries := countPipeDeliveries(t, env, "wc")
	for _, program := range []string{"grep needle lines.txt | wc -l", "find tree -type f | wc -l"} {
		t.Run(program, func(t *testing.T) {
			*deliveries = 0
			out, err := env.Call(t, t.Context(), program)
			testutil.FailErr(t, "run "+program, err)
			result := decodePipeOutcome(t, out)
			if result.Status != 0 || strings.TrimSpace(result.Stdout) != fmt.Sprint(records) {
				t.Fatalf("%s: %+v", program, result)
			}
			if *deliveries == 0 || *deliveries > records/100 {
				t.Fatalf("%s: %d records reached the pipe in %d deliveries", program, records, *deliveries)
			}
		})
	}
}
