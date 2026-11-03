package hostenv

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// invocation is one utility run: bound argv, buffered output and status.
type invocation struct {
	ctx     context.Context
	owners  *Owners
	tc      tools.ToolContext
	nested  commandprogram.NestedCommand
	bound   NativeBindings
	request commandprogram.OperationRequest
	spec    toolcontract.CommandSpecification
	argv    commandprogram.BoundArgv
	streams commandprogram.Streams
	out     strings.Builder
	errors  strings.Builder
	status  int
	// passthrough is env's nested command outcome, returned unchanged.
	passthrough *toolcontract.ProducerResult
}

func (inv *invocation) flag(key string) bool {
	_, ok := inv.argv.Last(key)
	return ok
}

func (inv *invocation) operands() commandprogram.Fields { return inv.argv.Operands() }

// line appends one stdout line under stream_bytes; nothing is truncated.
func (inv *invocation) line(text string) error {
	if observed := int64(inv.out.Len() + len(text) + 1); observed > inv.owners.stream {
		return tools.CommandLimitExceeded("stream_bytes", inv.owners.stream, observed)
	}
	inv.out.WriteString(text)
	inv.out.WriteByte('\n')
	return nil
}

// fail records an ordinary per-operand failure on stderr; the run continues.
func (inv *invocation) fail(format string, args ...any) {
	fmt.Fprintf(&inv.errors, inv.spec.Name+": "+format+"\n", args...)
	inv.status = max(inv.status, 1)
}

// usage refuses a form the catalog admits syntactically but whose operands are invalid.
func (inv *invocation) usage(word commandprogram.Field, expected string) error {
	return nativeutility.Usage(inv.spec, word, expected)
}

// unavailable refuses a host fact this platform does not expose; nothing is
// substituted for it.
func (inv *invocation) unavailable(capability string, cause error) error {
	reject := toolrejection.RejectInvalidArguments("COMMAND_PLATFORM_UNAVAILABLE", map[string]any{"command": inv.spec.Name, "capability": capability})
	if cause == nil {
		return reject
	}
	return fmt.Errorf("%w: %w", reject, cause)
}
