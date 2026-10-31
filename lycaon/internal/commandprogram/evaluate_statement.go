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
	inner := *f
	inner.errexitIgnored = f.errexitIgnored || s.Negated
	inner.procSubst = newProcSubstScope(f.procSubst)
	var descriptors *descriptorScope
	if len(s.Redirects) > 0 {
		descriptors = &descriptorScope{}
		prevented, err := descriptors.prepare(ctx, &inner, s)
		if prevented || err != nil {
			_ = inner.procSubst.finish(ctx, &inner, err)
			return signal{}, err
		}
	}
	sig, err := inner.command(ctx, s)
	if descriptors != nil {
		if !descriptors.opened.Load() && !descriptors.prevented && err == nil {
			_, err = (&evaluatorOperation{frame: &inner, statement: s, fields: Fields{{Value: ":", Span: s.Span}}}).invoke(ctx)
		}
		descriptors.closeEndpoints()
	}
	if procErr := inner.procSubst.finish(ctx, &inner, err); procErr != nil && err == nil {
		err = procErr
	}
	if descriptors != nil {
		_, stopped := f.state.latch.Stop()
		eligible := err == nil && !stopped && inner.publish.eligible()
		f.publish.mark(eligible)
		if finishErr := descriptors.finish(ctx, eligible); finishErr != nil && err == nil {
			err = finishErr
		}
	}
	if err == nil && s.Negated {
		f.state.control.Negate()
	}
	if err == nil && sig.kind == signalNext && f.exitsOnError(s) {
		return signal{}, f.state.control.Exit(f.state.control.Status())
	}
	return sig, err
}

// exitsOnError applies set -e after a statement: a nonzero status outside an
// ignored context exits. An && or || list is judged by its final command.
func (f *frame) exitsOnError(s commandsurface.Statement) bool {
	if !f.state.options.errexit || f.errexitIgnored || s.Negated || f.state.control.Exited() || f.state.control.Status() == 0 {
		return false
	}
	if binary, ok := s.Command.(commandsurface.BinaryCommand); ok && binary.Operator != "|" {
		return false
	}
	return true
}

func (f *frame) command(ctx context.Context, s commandsurface.Statement) (signal, error) {
	switch c := s.Command.(type) {
	case commandsurface.SimpleCommand:
		return f.simple(ctx, s, c)
	case commandsurface.EnvironmentDeclaration:
		return (&evaluatorOperation{frame: f, statement: s, fields: Fields{{Value: "export", Span: s.Span}}, body: func(ctx context.Context, g *frame) (signal, error) { return signal{}, g.exportDeclaration(ctx, s, c) }}).invoke(ctx)
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
	case commandsurface.CaseStatement:
		return f.caseStatement(ctx, s, c)
	case commandsurface.FunctionDeclaration:
		f.state.functions[c.Name] = declaredFunction{decl: c, unit: f.unit, sourceUnit: f.parent.SourceUnitID}
		return signal{}, f.builtinStatus(s.SyntaxLocation, "function", 0)
	case commandsurface.TestExpression:
		return (&evaluatorOperation{frame: f, statement: s, fields: Fields{{Value: "[[", Span: s.Span}}, body: func(ctx context.Context, g *frame) (signal, error) { return signal{}, g.testExpression(ctx, s, c) }}).invoke(ctx)
	case commandsurface.ArithmeticCommand:
		return (&evaluatorOperation{frame: f, statement: s, fields: Fields{{Value: "((", Span: s.Span}}, body: func(ctx context.Context, g *frame) (signal, error) {
			return signal{}, runArithmeticCommand(ctx, g, s, c)
		}}).invoke(ctx)
	case commandsurface.TimedCommand:
		return runTimed(ctx, f, s, c)
	case commandsurface.TrapCommand:
		return (&evaluatorOperation{frame: f, statement: s, fields: Fields{{Value: "trap", Span: s.Span}}, body: func(_ context.Context, g *frame) (signal, error) { return signal{}, runTrapCommand(g, s, c) }}).invoke(ctx)
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
	left := *f
	left.errexitIgnored = true
	sig, err := left.statement(ctx, c.Left)
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
	test := *f
	test.errexitIgnored = true
	sig, err := test.statements(ctx, list)
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
	case signalNext:
	}
	return signal{}, false, nil
}

// Loop status is the last body status, or 0 when the body never ran. Each test
// evaluation and each item is one loop_iterations unit charged before it runs.
// while runs the body after a zero condition status, until after a nonzero one.
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
		if ok == c.Until {
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
		var fields Fields
		prevented, err := f.expanding(s.SyntaxLocation, func(g *frame) error {
			var err error
			fields, err = g.expandWords(ctx, c.Items)
			return err
		})
		if prevented || err != nil {
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
		if err := f.state.vars.Assign(ctx, c.Variable, item); err != nil {
			return signal{}, f.expansionRefusal(s.SyntaxLocation, err)
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
	run, err := f.beginRecorded(ctx, s.SyntaxLocation, "( )")
	if err != nil {
		return err
	}
	defer run.close(ctx)
	g, release, err := f.isolate(ctx, nil)
	if err != nil {
		return f.stateRefusal(s.SyntaxLocation, err)
	}
	defer release()
	g.parent, g.loops, g.returnable = run.inv, 0, false
	_, err = g.statements(ctx, list)
	if trapErr := runExitTrap(ctx, g, err); trapErr != nil && err == nil {
		err = trapErr
	}
	f.publish.mark(g.publish.eligible())
	return f.settleFrame(ctx, run, g, err)
}

// settleFrame records a nested frame's status in this frame as one execution.
func (f *frame) settleFrame(ctx context.Context, run *childRun, g *frame, err error) error {
	result := ordinaryResult(g.state.control.Status())
	if stop, ok := g.state.latch.Stop(); ok {
		result = stop.Result
	}
	run.settle(ctx, result, err)
	if err != nil {
		return err
	}
	return f.state.control.Record(run.inv.Identity.ExecutionID, result, true, false)
}

// builtinStatus records the ordinary status of evaluator bookkeeping that
// invokes no operation (assignments, declarations, loop and branch results,
// builtin usage errors). It is not a child execution and adds no trace event.
func (f *frame) builtinStatus(loc commandsurface.SyntaxLocation, command string, status int) error {
	if len(f.state.pipeStatus) == 1 {
		f.state.pipeStatus[0] = status
	} else {
		f.state.pipeStatus = []int{status}
	}
	f.state.vars.SetPipeStatus(f.state.pipeStatus)
	id := string(loc.ID)
	if id == "" {
		id = command
	}
	if id == "" || status < 0 || status > 255 {
		id = uuid.NewString()
	}
	return f.state.control.Record(id, ordinaryResult(status), true, false)
}

// stateRefusal turns an expansion, variable or memory admission refusal into a
// latched stop located at the refused word when the refusal names one.
func (f *frame) stateRefusal(loc commandsurface.SyntaxLocation, err error) error {
	var address *AddressError
	var coded interface{ MachineErrorCode() string }
	if errors.As(err, &address) && errors.As(address.Cause, &coded) {
		if address.Span != (toolcontract.SourceSpan{}) {
			loc.Span = address.Span
		}
		if f.io.streams.Error != nil {
			_, _ = fmt.Fprintf(f.io.streams.Error, "@%s: not attached here [%s]\n", address.Label, coded.MachineErrorCode())
		}
		return f.refuse(loc, coded.MachineErrorCode(), "an attached folder label", err)
	}
	var expansion *ExpansionError
	if errors.As(err, &expansion) {
		if expansion.Span != (toolcontract.SourceSpan{}) {
			loc.Span = expansion.Span
		}
		return f.refuse(loc, expansion.Code, expansion.Expected, err)
	}
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

// caseStatement matches the expanded word against each item's glob patterns
// in order and runs the first matching body; no match is status 0.
func (f *frame) caseStatement(ctx context.Context, s commandsurface.Statement, c commandsurface.CaseStatement) (signal, error) {
	var subject string
	prevented, err := f.expanding(s.SyntaxLocation, func(g *frame) error {
		var err error
		subject, err = g.e.expander.ExpandScalar(ctx, g, c.Word)
		return err
	})
	if prevented || err != nil {
		return signal{}, err
	}
	patterns := f.predicates().Patterns
	if patterns == nil {
		return signal{}, f.refuse(s.SyntaxLocation, "COMMAND_SYNTAX_UNSUPPORTED", "a predicate pattern owner for case", nil)
	}
	for _, item := range c.Items {
		for _, word := range item.Patterns {
			var value PredicateValue
			prevented, err := f.expanding(item.SyntaxLocation, func(g *frame) error {
				var err error
				value, err = g.e.expander.ExpandPattern(ctx, g, word)
				return err
			})
			if prevented || err != nil {
				return signal{}, err
			}
			matched, err := patterns.MatchPredicate(ctx, "glob", subject, value)
			if err != nil {
				return signal{}, f.stateRefusal(item.SyntaxLocation, err)
			}
			if matched {
				if len(item.Body) == 0 {
					return signal{}, f.builtinStatus(s.SyntaxLocation, "case", 0)
				}
				return f.statements(ctx, item.Body)
			}
		}
	}
	return signal{}, f.builtinStatus(s.SyntaxLocation, "case", 0)
}
