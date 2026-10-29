package textutil

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// invocation is one utility run: bound argv, metered output and status.
type invocation struct {
	ctx     context.Context
	owners  *Owners
	ports   Ports
	request commandprogram.OperationRequest
	spec    toolcontract.CommandSpecification
	argv    commandprogram.BoundArgv
	// streams has non-nil Input and Error; Output is reached through out.
	streams   commandprogram.Streams
	out       *nativeutility.Output
	errors    strings.Builder
	status    int
	published bool
	uncertain bool
	releases  []func()
	sed       *preparedSed
	preparing bool
	// The ordered xargs settlement loop owns the first stopped child outcome.
	stoppedBatch *toolcontract.ProducerResult
}

func newInvocation(ctx context.Context, p Prepared, ports Ports, streams commandprogram.Streams) *invocation {
	inv := &invocation{ctx: ctx, owners: p.owners, ports: ports, request: p.request, spec: p.spec, argv: p.Argv, streams: streams, sed: p.sed}
	if p.sed != nil {
		inv.releases = append(inv.releases, p.Close)
	}
	if inv.streams.Input == nil {
		inv.streams.Input = commandprogram.NullInput()
	}
	if inv.streams.Error == nil {
		inv.streams.Error = io.Discard
	}
	inv.out = nativeutility.NewOutput(ctx, streams.Output, "stream_throughput_bytes", p.owners.limits["stream_throughput_bytes"])
	return inv
}

func (inv *invocation) flag(key string) bool {
	_, ok := inv.argv.Last(key)
	return ok
}

func (inv *invocation) value(key string) (commandprogram.Field, bool) {
	option, ok := inv.argv.Last(key)
	if !ok || len(option.Values) == 0 {
		return commandprogram.Field{}, false
	}
	return option.Values[0], true
}

func (inv *invocation) operands() commandprogram.Fields { return inv.argv.Operands() }

func (inv *invocation) limit(id string) int64 { return inv.owners.limits[id] }

func (inv *invocation) usage(word commandprogram.Field, expected string) error {
	return nativeutility.Usage(inv.spec, word, expected)
}

func unsupported(inv *invocation, word commandprogram.Field, expected string) error {
	return nativeutility.Unsupported(inv.spec, word, expected)
}

// commandWord locates refusals that concern the whole invocation.
func (inv *invocation) commandWord() commandprogram.Field { return inv.request.Argv[0] }

// reserve charges working storage to the program before it is allocated.
func (inv *invocation) reserve(n int64) error {
	release, err := inv.ports.Memory.Reserve(inv.ctx, n)
	if err != nil {
		return err
	}
	inv.releases = append(inv.releases, release)
	return nil
}

func (inv *invocation) close() {
	for i := len(inv.releases) - 1; i >= 0; i-- {
		inv.releases[i]()
	}
	inv.releases = nil
}

// fail records an ordinary per-operand failure on stderr; the run continues.
func (inv *invocation) fail(status int, operand, reason string) {
	if inv.spec.Name == "iconv" {
		fmt.Fprintf(&inv.errors, "iconv: cannot open input file `%s': %s\n", operand, reason)
	} else {
		fmt.Fprintf(&inv.errors, "%s: %s: %s\n", inv.spec.Name, operand, reason)
	}
	inv.status = max(inv.status, status)
}
