package commandprogram

import (
	"context"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// evaluatorOperation enters the host execution boundary before local shell
// state or input is consumed. The evaluator retains its actual child identity.
type evaluatorOperation struct {
	frame       *frame
	statement   commandsurface.Statement
	fields      Fields
	assignments []assignment
	body        func(context.Context, *frame) (signal, error)
}

func (o *evaluatorOperation) invoke(ctx context.Context) (signal, error) {
	f := o.frame
	if o.localState() && !f.io.pending() {
		if f.io.scope != nil {
			streams, err := f.io.PrepareStreams(ctx)
			if err != nil {
				return signal{}, err
			}
			g := *f
			g.io.streams = streams
			if o.body != nil {
				return o.body(ctx, &g)
			}
			return o.execute(ctx, &g)
		}
		if o.body != nil {
			return o.body(ctx, f)
		}
		return o.execute(ctx, f)
	}
	return o.invokeHost(ctx)
}

func (o *evaluatorOperation) invokeHost(ctx context.Context) (signal, error) {
	f, s, fields := o.frame, o.statement, o.fields
	if len(fields) == 0 {
		fields = Fields{{Value: ":", Span: s.Span}}
	}
	name := fields[0].Value
	child, err := f.begin(s.SyntaxLocation, name)
	if err != nil {
		return signal{}, err
	}
	request := OperationRequest{Invocation: child.inv, Statement: s, OperationID: name, Argv: fields, StreamPreparation: f.io}
	var sig signal
	result, err := f.state.dispatch.InvokeEvaluator(ctx, request, f.io.streams, func(ctx context.Context, streams Streams) (toolcontract.ProducerResult, error) {
		g := *f
		g.io.streams = streams
		g.activeHost = child
		var runErr error
		if o.body != nil {
			sig, runErr = o.body(ctx, &g)
		} else {
			sig, runErr = o.execute(ctx, &g)
		}
		if child.settled {
			return child.record.Result, runErr
		}
		if stop, ok := g.state.latch.Stop(); ok {
			return stop.Result, runErr
		}
		return ordinaryResult(g.state.control.Status()), runErr
	})
	if child.settled {
		return sig, err
	}
	return sig, f.settle(ctx, child, result, err)
}
func (o *evaluatorOperation) execute(ctx context.Context, f *frame) (signal, error) {
	s, fields, assigns := o.statement, o.fields, o.assignments
	if len(fields) == 0 {
		for _, a := range assigns {
			if err := a.apply(ctx, f.state.vars); err != nil {
				return signal{}, f.expansionRefusal(a.loc, err)
			}
		}
		return signal{}, f.builtinStatus(s.SyntaxLocation, "assignment", 0)
	}
	name := fields[0].Value
	if len(assigns) > 0 {
		g := *f
		g.prefix = make(map[string]string, len(f.prefix)+len(assigns))
		for name, value := range f.prefix {
			g.prefix[name] = value
		}
		for _, a := range assigns {
			g.prefix[a.name] = a.value
		}
		f = &g
	}
	if controlBuiltins[name] {
		return f.controlBuiltin(s, name, fields)
	}
	if decl, ok := f.state.functions[name]; ok {
		return signal{}, f.call(ctx, s, decl, fields, assigns)
	}
	switch name {
	case "eval":
		return f.eval(ctx, s, fields)
	case "source":
		return signal{}, f.source(ctx, s, fields)
	case "true", "false", ":":
		return signal{}, f.inProcess(ctx, s, name, func(context.Context, toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
			return BooleanBuiltin(ctx, name, fields.Values()[1:])
		})
	case "test", "[":
		return signal{}, f.inProcess(ctx, s, name, func(ctx context.Context, inv toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
			return f.predicates().TestBuiltin(ctx, inv, name, fields.Values()[1:])
		})
	case "unset":
		return signal{}, f.unset(s, fields)
	case "set":
		return signal{}, f.setOptions(s, fields)
	case "read":
		return signal{}, f.readBuiltin(ctx, s, fields)
	case "ulimit":
		return signal{}, runUlimit(ctx, f, s, fields)
	}
	return signal{}, f.dispatch(ctx, s, fields, assigns)
}

// Local language state has no stream operation of its own. Pending redirects
// still require admission before any of these state changes take place.
func (o *evaluatorOperation) localState() bool {
	if len(o.fields) == 0 {
		return true
	}
	name := o.fields[0].Value
	if controlBuiltins[name] {
		return true
	}
	switch name {
	case ":":
		return !(len(o.fields) == 2 && o.fields[1].Value == "--help")
	case "true", "false":
		return len(o.fields) == 1
	case "set", "unset", "export", "trap", "((":
		return true
	case "test", "[":
		for _, f := range o.fields[1:] {
			if predicateFileUnary(f.Value) {
				return false
			}
		}
		return true
	case "[[":
		if c, ok := o.statement.Command.(commandsurface.TestExpression); ok {
			return !testNodeUsesFiles(c.Expression)
		}
	}
	return false
}

func predicateFileUnary(op string) bool {
	switch op {
	case "-e", "-f", "-d", "-L", "-r", "-w", "-x", "-s":
		return true
	}
	return false
}

func testNodeUsesFiles(n commandsurface.TestNode) bool {
	switch n := n.(type) {
	case commandsurface.GroupedPredicate:
		return testNodeUsesFiles(n.Expression)
	case commandsurface.UnaryPredicate:
		if predicateFileUnary(n.Operator) {
			return true
		}
		return testNodeUsesFiles(n.Operand)
	case commandsurface.BinaryPredicate:
		return testNodeUsesFiles(n.Left) || testNodeUsesFiles(n.Right)
	}
	return false
}
