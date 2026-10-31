package toolhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Observe the real owner error without replacing the reader, refusal, or result.
type resourceRefusalObserver struct {
	owner   commandprogram.OperationDispatcher
	observe func(*toolrejection.ToolReject)
}

func (o resourceRefusalObserver) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	result, err := o.owner.Invoke(ctx, request, streams)
	if reject := toolrejection.AsToolReject(err); reject != nil {
		o.observe(reject)
	}
	return result, err
}

func TestCommandResourceRefusalPreservesFactsAndEarlierEffects(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "guidance.LoadHintConfigStock failed", err)
	if err = guidance.ValidateHintConfig(hints); err != nil {
		testutil.FailErr(t, "guidance.ValidateHintConfig failed", err)
	}
	env.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	if err := os.WriteFile(filepath.Join(env.Dir, "record"), bytes.Repeat([]byte{'x'}, (4<<20)+1), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	owners := env.Runtime.Owners
	var refusal *toolrejection.ToolReject
	var observed int
	env.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
		bound, err := owners(tc, paths, memory, admission)
		if err != nil {
			return bound, err
		}
		install := bound.Builtins
		bound.Builtins = func(ports command.BuiltinPorts) (map[string]commandprogram.OperationDispatcher, func(), error) {
			builtins, release, err := install(ports)
			if owner := builtins["cut"]; owner != nil {
				builtins["cut"] = resourceRefusalObserver{owner: owner, observe: func(reject *toolrejection.ToolReject) { refusal = reject; observed++ }}
			}
			return builtins, release, err
		}
		return bound, nil
	}
	out, err := env.Call(t, t.Context(), `printf before > kept; cut -f1 record; printf after > never`)
	testutil.FailErr(t, "env.Call failed", err)
	result := decodePipeOutcome(t, out)
	if observed != 1 || refusal == nil || refusal.Code != "COMMAND_LIMIT_EXCEEDED" || refusal.Data["limit_id"] != "record_bytes" {
		t.Fatalf("owner refusal %d %+v", observed, refusal)
	}
	if maximum, ok := refusal.Data["maximum"].(int64); !ok || maximum != 4<<20 {
		t.Fatalf("limit facts %+v", refusal.Data)
	}
	if used, ok := refusal.Data["observed"].(int64); !ok || used <= 4<<20 {
		t.Fatalf("observed limit facts %+v", refusal.Data)
	}
	if result.Refusal == nil || result.Refusal.Code != refusal.Code || result.Refusal.Feedback == "" || result.StopCode != refusal.Code || result.State == "completed" || result.Status == 0 {
		t.Fatalf("delivered refusal %+v", result)
	}
	t.Logf("Delivered resource refusal:\n%s", result.Refusal.Feedback)
	const stderr = "cut: request: This command reached its record_bytes limit (4194305 bytes; maximum 4194304) [COMMAND_LIMIT_EXCEEDED]\n"
	if result.Stdout != "" || result.Stderr != stderr || result.CompletedEffects != 1 || result.UncertainEffects != 0 {
		t.Fatalf("partial outcome %+v, want stderr %q", result, stderr)
	}
	if kept, err := os.ReadFile(filepath.Join(env.Dir, "kept")); err != nil || string(kept) != "before" {
		t.Fatalf("earlier effect %q %v", kept, err)
	}
	if _, err := os.Stat(filepath.Join(env.Dir, "never")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("continuation ran: %v", err)
	}
}
