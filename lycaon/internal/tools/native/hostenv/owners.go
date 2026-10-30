// Package hostenv is the command frontend for the invocation environment,
// command discovery and host identity utilities: env, printenv, which,
// command, type, pwd, uname, nproc, whoami, id, hostname and date. Each binds argv
// through the catalog and answers from the request's frozen environment, the
// shared launch lookup, the invocation's logical paths and injected host
// facts. No utility launches a process or reads ambient shell state.
package hostenv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// Owners serves every program; per-invocation state lives in Prepared.
type Owners struct {
	// Host answers identity and system queries from actual platform facts.
	Host HostFacts
	// Clock is read once per date operation.
	Clock func() time.Time
	// Boundary is the read boundary pwd binds the invocation directory under.
	Boundary *sandbox.Boundary

	catalog toolcontract.LanguageCatalog
	specs   map[string]toolcontract.CommandSpecification
	natives map[string]bool
	stream  int64
}

type utility func(*invocation) error

var utilities = map[string]utility{
	"env": runEnv, "printenv": runPrintenv,
	"which": runWhich, "command": runCommand, "type": runType,
	"pwd": runPwd, "uname": runUname, "nproc": runNproc, "whoami": runWhoami, "id": runID, "hostname": runHostname, "date": runDate,
}

// NewOwners binds the frozen catalog. Host and Clock are required.
func NewOwners(host HostFacts, clock func() time.Time, boundary *sandbox.Boundary) (*Owners, error) {
	if host == nil || clock == nil {
		return nil, errors.New("host environment utilities require host facts and a clock")
	}
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return nil, err
	}
	o := &Owners{Host: host, Clock: clock, Boundary: boundary, catalog: catalog, specs: map[string]toolcontract.CommandSpecification{}, natives: map[string]bool{}}
	for _, command := range catalog.Commands {
		if strings.Contains(command.Name, " ") {
			continue
		}
		o.natives[command.Name] = true
		for _, alias := range command.Aliases {
			o.natives[alias] = true
		}
		if _, ok := utilities[command.Name]; ok {
			o.specs[command.Name] = command
		}
	}
	for name := range utilities {
		if _, ok := o.specs[name]; !ok {
			return nil, fmt.Errorf("host environment utility %s is not in the language catalog", name)
		}
	}
	for _, limit := range catalog.Language.Limits {
		if limit.ID == "stream_bytes" {
			o.stream = limit.Maximum
		}
	}
	if o.stream < 1 {
		return nil, errors.New("language catalog is missing stream_bytes")
	}
	return o, nil
}

// Commands lists the catalog commands these owners serve, in catalog order.
func (o *Owners) Commands() []string {
	out := make([]string, 0, len(o.specs))
	for _, command := range o.catalog.Commands {
		if _, ok := o.specs[command.Name]; ok {
			out = append(out, command.Name)
		}
	}
	return out
}

// Builtins returns one in-process dispatcher per utility for one invocation's
// tool context, the shape the program runtime composes. nested runs env's
// COMMAND operand as its own child; without it that form is refused.
func (o *Owners) Builtins(tc tools.ToolContext, nested commandprogram.NestedCommand, bound NativeBindings) map[string]commandprogram.OperationDispatcher {
	out := make(map[string]commandprogram.OperationDispatcher, len(o.specs))
	for name := range o.specs {
		out[name] = dispatcher{owners: o, tc: tc, nested: nested, bound: bound}
	}
	return out
}

// NativeBindings reports whether this program binds a name to a native owner;
// type, which and command -v describe what dispatch actually does.
type NativeBindings interface {
	NativeBound(name string) bool
}

type dispatcher struct {
	owners *Owners
	tc     tools.ToolContext
	nested commandprogram.NestedCommand
	bound  NativeBindings
}

func (d dispatcher) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	prepared, err := d.owners.Prepare(request)
	if err != nil {
		return settle(err, 0), err
	}
	return prepared.InvokeOwner(ctx, d.tc, d.nested, d.bound, streams)
}

func (d dispatcher) EffectMode(_ context.Context, request commandprogram.OperationRequest) string {
	prepared, err := d.owners.Prepare(request)
	if err != nil {
		return ""
	}
	return prepared.Mode
}

// Prepared is one bound utility invocation; it performs no query until run.
type Prepared struct {
	Mode    string
	Argv    commandprogram.BoundArgv
	Review  commandprogram.ArgvReview
	owners  *Owners
	request commandprogram.OperationRequest
	spec    toolcontract.CommandSpecification
	run     utility
}

// Prepare binds a reached command through the catalog option grammar. The
// operation identity of these utilities is the command name.
func (o *Owners) Prepare(request commandprogram.OperationRequest) (Prepared, error) {
	if err := request.Validate(); err != nil {
		return Prepared{}, err
	}
	command := request.Argv[0].Value
	spec, ok := o.specs[command]
	if !ok {
		return Prepared{}, fmt.Errorf("%s is not a host environment command", command)
	}
	if request.OperationID != spec.Name {
		return Prepared{}, fmt.Errorf("%s belongs to operation %s, not %s", command, spec.Name, request.OperationID)
	}
	bound, err := commandprogram.CatalogBinder{}.Bind(spec, request.Argv)
	if err != nil {
		return Prepared{}, nativeutility.ArgvRejection(err, spec)
	}
	mode := ""
	if len(spec.Effects) > 0 {
		mode = spec.Effects[0].Mode
	}
	if command == "env" {
		mode = "inspect"
		for _, op := range bound.Operands() {
			if idx := strings.IndexByte(op.Value, '='); idx <= 0 {
				mode = "dispatch"
				break
			}
		}
	}
	return Prepared{Mode: mode, Argv: bound, Review: bound.Review(spec.Name), owners: o, request: request, spec: spec, run: utilities[command]}, nil
}

// InvokeOwner runs the bound utility once. Stdout is written only after the
// complete result is known, so a refusal never leaves a plausible prefix.
func (p Prepared) InvokeOwner(ctx context.Context, tc tools.ToolContext, nested commandprogram.NestedCommand, bound NativeBindings, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	if p.run == nil {
		return settle(errors.New("host environment invocation is not prepared"), 0), errors.New("host environment invocation is not prepared")
	}
	if err := ctx.Err(); err != nil {
		return settle(err, 0), err
	}
	inv := &invocation{ctx: ctx, owners: p.owners, tc: tc, nested: nested, bound: bound, request: p.request, spec: p.spec, argv: p.Argv, streams: streams}
	err := p.run(inv)
	if inv.passthrough != nil {
		return *inv.passthrough, err
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && inv.out.Len() > 0 && streams.Output != nil {
		_, err = streams.Output.Write([]byte(inv.out.String()))
	}
	if err == nil && inv.errors.Len() > 0 && streams.Error != nil {
		_, _ = streams.Error.Write([]byte(inv.errors.String()))
	}
	return settle(err, inv.status), err
}

func settle(err error, status int) toolcontract.ProducerResult {
	return nativeutility.Settle(err, status, false)
}
