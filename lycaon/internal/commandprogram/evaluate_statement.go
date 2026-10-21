package commandprogram

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// signal carries break, continue and return out of nested statements. levels
// counts the remaining enclosing loops for break N and continue N.
type signal struct {
	kind   signalKind
	levels int
}

type signalKind uint8

const (
	signalNext signalKind = iota
	signalBreak
	signalContinue
	signalReturn
)

func (f *frame) statements(ctx context.Context, list []commandsurface.Statement) (signal, error) {
	for _, s := range list {
		sig, err := f.statement(ctx, s)
		if err != nil || sig.kind != signalNext || f.state.control.Exited() {
			return sig, err
		}
	}
	return signal{}, nil
}

// statement charges the visit before expanding or admitting anything inside it,
// so even an empty loop body is bounded by host counters.
func (f *frame) statement(ctx context.Context, s commandsurface.Statement) (signal, error) {
	if err := ctx.Err(); err != nil {
		return signal{}, f.interrupt(err)
	}
	run, err := f.state.control.ShouldRun(StatusSequence)
	if err != nil || !run {
		return signal{}, err
	}
	if err := f.e.charge(f, s.SyntaxLocation, "statement_visits", 1); err != nil {
		return signal{}, err
	}
	if s.Background {
		return signal{}, f.background(ctx, s)
	}
	sig, err := f.redirected(ctx, s, func(g *frame) (signal, error) { return g.command(ctx, s) })
	if err == nil && s.Negated {
		f.state.control.Negate()
	}
	return sig, err
}

func (f *frame) command(ctx context.Context, s commandsurface.Statement) (signal, error) {
	switch c := s.Command.(type) {
	case commandsurface.SimpleCommand:
		return f.simple(ctx, s, c)
	case commandsurface.EnvironmentDeclaration:
		return signal{}, f.exportDeclaration(ctx, s, c)
	case commandsurface.StatementGroup:
		if !c.Isolated {
			return f.statements(ctx, c.Statements)
		}
		return signal{}, f.isolated(ctx, s, c.Statements)
	case commandsurface.BinaryCommand:
		return f.binary(ctx, s, c)
	case commandsurface.Conditional:
		return f.conditional(ctx, s, c)
	case commandsurface.WhileLoop:
		return f.whileLoop(ctx, s, c)
	case commandsurface.ForLoop:
		return f.forLoop(ctx, s, c)
	case commandsurface.FunctionDeclaration:
		f.state.functions[c.Name] = c
		return signal{}, f.builtinStatus(s.SyntaxLocation, "function", 0)
	case commandsurface.TestExpression:
		return signal{}, f.testExpression(ctx, s, c)
	case nil:
		return signal{}, nil
	default:
		return signal{}, f.refuse(s.SyntaxLocation, "COMMAND_SYNTAX_UNSUPPORTED", "a supported version 1 command", fmt.Errorf("%T", c))
	}
}

func (f *frame) binary(ctx context.Context, s commandsurface.Statement, c commandsurface.BinaryCommand) (signal, error) {
	connector := StatusAnd
	switch c.Operator {
	case "|":
		return signal{}, f.pipeline(ctx, s, c)
	case "&&":
	case "||":
		connector = StatusOr
	default:
		return signal{}, f.refuse(c.SyntaxLocation, "COMMAND_SYNTAX_UNSUPPORTED", "|, && or ||", nil)
	}
	sig, err := f.statement(ctx, c.Left)
	if err != nil || sig.kind != signalNext {
		return sig, err
	}
	run, err := f.state.control.ShouldRun(connector)
	if err != nil || !run {
		return signal{}, err
	}
	return f.statement(ctx, c.Right)
}

// condition evaluates a test list; its ordinary status selects the branch.
func (f *frame) condition(ctx context.Context, list []commandsurface.Statement) (bool, signal, error) {
	sig, err := f.statements(ctx, list)
	if err != nil || sig.kind != signalNext || f.state.control.Exited() {
		return false, sig, err
	}
	return f.state.control.Status() == 0, signal{}, nil
}

func (f *frame) conditional(ctx context.Context, s commandsurface.Statement, c commandsurface.Conditional) (signal, error) {
	for branch := &c; branch != nil; branch = branch.Else {
		taken := true
		if len(branch.Condition) > 0 {
			ok, sig, err := f.condition(ctx, branch.Condition)
			if err != nil || sig.kind != signalNext || f.state.control.Exited() {
				return sig, err
			}
			taken = ok
		}
		if taken {
			return f.statements(ctx, branch.Then)
		}
	}
	return signal{}, f.builtinStatus(s.SyntaxLocation, "if", 0)
}

// loopBody runs one iteration and resolves break/continue addressed to it.
// done reports that the loop must stop iterating.
func (f *frame) loopBody(ctx context.Context, body []commandsurface.Statement) (sig signal, done bool, err error) {
	inner := *f
	inner.loops++
	sig, err = inner.statements(ctx, body)
	if err != nil || f.state.control.Exited() {
		return signal{}, true, err
	}
	switch sig.kind {
	case signalBreak:
		if sig.levels > 1 {
			return signal{kind: signalBreak, levels: sig.levels - 1}, true, nil
		}
		return signal{}, true, nil
	case signalContinue:
		if sig.levels > 1 {
			return signal{kind: signalContinue, levels: sig.levels - 1}, true, nil
		}
	case signalReturn:
		return sig, true, nil
	}
	return signal{}, false, nil
}

// Loop status is the last body status, or 0 when the body never ran. Each test
// evaluation and each item is one loop_iterations unit charged before it runs.
func (f *frame) whileLoop(ctx context.Context, s commandsurface.Statement, c commandsurface.WhileLoop) (signal, error) {
	status := 0
	for {
		if err := ctx.Err(); err != nil {
			return signal{}, f.interrupt(err)
		}
		if err := f.e.charge(f, s.SyntaxLocation, "loop_iterations", 1); err != nil {
			return signal{}, err
		}
		ok, sig, err := f.condition(ctx, c.Condition)
		if err != nil || sig.kind != signalNext || f.state.control.Exited() {
			return sig, err
		}
		if !ok {
			break
		}
		sig, done, err := f.loopBody(ctx, c.Body)
		status = f.state.control.Status()
		if err != nil || sig.kind != signalNext {
			return sig, err
		}
		if done {
			break
		}
	}
	return signal{}, f.loopStatus(s, status)
}

func (f *frame) forLoop(ctx context.Context, s commandsurface.Statement, c commandsurface.ForLoop) (signal, error) {
	items := f.Positional()
	if !c.Positional {
		fields, err := f.expandWords(ctx, c.Items)
		if failure := substitutionFailure(err); failure != nil {
			return signal{}, f.builtinStatus(s.SyntaxLocation, "for", failure.Status)
		}
		if err != nil {
			return signal{}, err
		}
		items = fields.Values()
	}
	status := 0
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return signal{}, f.interrupt(err)
		}
		if err := f.e.charge(f, s.SyntaxLocation, "loop_iterations", 1); err != nil {
			return signal{}, err
		}
		if err := f.state.vars.set(ctx, c.Variable, item, false); err != nil {
			return signal{}, f.stateRefusal(s.SyntaxLocation, err)
		}
		sig, done, err := f.loopBody(ctx, c.Body)
		status = f.state.control.Status()
		if err != nil || sig.kind != signalNext {
			return sig, err
		}
		if done {
			break
		}
	}
	return signal{}, f.loopStatus(s, status)
}

func (f *frame) loopStatus(s commandsurface.Statement, status int) error {
	if f.state.control.Exited() {
		return nil
	}
	return f.builtinStatus(s.SyntaxLocation, "loop", status)
}

// isolated runs ( ... ): variables, functions and directory are copied and its
// exit leaves only the group. Host stops still latch the shared program.
func (f *frame) isolated(ctx context.Context, s commandsurface.Statement, list []commandsurface.Statement) error {
	run, err := f.begin(s.SyntaxLocation, "( )")
	if err != nil {
		return err
	}
	defer run.close()
	g, release, err := f.isolate(ctx, nil)
	if err != nil {
		return f.stateRefusal(s.SyntaxLocation, err)
	}
	defer release()
	g.parent, g.loops, g.returnable = run.inv, 0, false
	_, err = g.statements(ctx, list)
	f.publish.mark(g.publish.eligible())
	return f.settleFrame(run, g, err)
}

// settleFrame records a nested frame's status in this frame as one execution.
func (f *frame) settleFrame(run *childRun, g *frame, err error) error {
	result := ordinaryResult(g.state.control.Status())
	if stop, ok := g.state.latch.Stop(); ok {
		result = stop.Result
	}
	run.settle(result, err)
	if err != nil {
		return err
	}
	return f.state.control.Record(run.inv.Identity.ExecutionID, result, true, false)
}

// builtinStatus records the ordinary status of evaluator bookkeeping that
// invokes no operation (assignments, declarations, loop and branch results,
// builtin usage errors). It is not a child execution and adds no trace event.
func (f *frame) builtinStatus(loc commandsurface.SyntaxLocation, command string, status int) error {
	return f.state.control.Record(uuid.NewString(), ordinaryResult(status), true, false)
}

// stateRefusal turns a variable/memory admission refusal into a latched stop.
func (f *frame) stateRefusal(loc commandsurface.SyntaxLocation, err error) error {
	var limit *ProgramLimitError
	if errors.As(err, &limit) {
		return f.refuseLimit(loc, limit.Limit, limit.Observed)
	}
	var memory *toolcontract.ProgramMemoryLimitError
	if errors.As(err, &memory) {
		return f.refuse(loc, limitExceeded, "bounded program memory", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return f.interrupt(err)
	}
	return err
}
