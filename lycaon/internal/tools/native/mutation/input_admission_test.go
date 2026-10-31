package mutation

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestInputRedirectProjectsCaptureAdmissionBeforeDeliveringBytes(t *testing.T) {
	for _, test := range []struct {
		name, account, limitID            string
		working, state, maximum, observed int64
	}{
		{"working", "working", "evaluation_memory_bytes", (32 << 10) - 1, 4096, (32 << 10) - 1, 32 << 10},
		{"proof state", "state", "program_state_bytes", 32 << 10, 255, 255, 256},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{"in.txt": "original bytes\n"})
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: test.working, State: test.state})
			testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
			h.owners.Revisions.Memory = memory
			inv := h.invocation()
			span := toolcontract.SourceSpan{StartByte: 10, EndByte: 16, Line: 1, Column: 11}
			input, err := h.publication().OpenInput(t.Context(), commandprogram.InputDestination{Invocation: inv, Target: commandprogram.Field{Value: "in.txt", Span: span}})
			if input != nil {
				_ = input.Close()
				t.Fatal("refused capture returned a reader")
			}
			reject := toolrejection.AsToolReject(err)
			var cause *toolcontract.ProgramMemoryLimitError
			if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || !errors.As(err, &cause) || cause.Account != test.account || cause.Maximum != test.maximum || cause.Observed != test.observed {
				t.Fatalf("capture refusal %v cause %+v", err, cause)
			}
			if reject.Data["limit_id"] != test.limitID || reject.Data["maximum"] != test.maximum || reject.Data["observed"] != test.observed || reject.Data["source_start_byte"] != span.StartByte || reject.Data["source_column"] != span.Column {
				t.Fatalf("redirect facts %+v", reject.Data)
			}
			if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) || len(h.pins.pins[inv.Identity.ExecutionID]) != 0 {
				t.Fatalf("refused capture retained state %+v pins %+v", memory.InUse(), h.pins.pins)
			}
		})
	}
}
