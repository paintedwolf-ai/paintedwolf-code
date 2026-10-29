package commandprogram

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// stagedRedirect is one opened output destination and its own execution.
type stagedRedirect struct {
	frame       *frame
	run         *childRun
	operator    string
	target      string
	errorStream io.Writer
	output      StagedOutput
}

func (o *stagedRedirect) finish(ctx context.Context, publish bool) error {
	if !publish {
		err := o.output.Abandon(ctx)
		o.run.settle(ctx, toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err)
		return nil
	}
	published, err := o.output.Publish(ctx)
	if err != nil {
		result := ownerResult(err)
		if report := o.frame.e.ports.Refusals; report != nil {
			report(o.errorStream, RefusalReport{Operation: o.operator, Operand: o.target, Code: result.StopCode, Span: o.run.inv.Span, Cause: err})
		}
		return o.frame.settle(ctx, o.run, result, err)
	}
	certainty := toolcontract.EffectCompleted
	if published.PublishedRevision == "" {
		certainty = toolcontract.NoEffect
	}
	o.run.settle(ctx, toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: certainty, Evidence: toolcontract.OperationEvidence{OwnerRef: published.PublishedRevision}}, nil)
	return nil
}

// bind applies one redirect to g.io.streams. failed reports an ordinary failure
// (for example a failed target substitution or a refused open) after which the
// command is not run; err reports a host stop.
func (s *descriptorScope) bind(ctx context.Context, g *frame, r commandsurface.ProgramRedirect, field Field) (*stagedRedirect, bool, error) {
	if err := g.e.charge(g, r.SyntaxLocation, "statement_visits", 1); err != nil {
		return nil, false, err
	}
	switch r.Operator {
	case ">&":
		src, _ := strconv.Atoi(staticWord(r.Target))
		if !g.io.streams.hasWriter(src) {
			return nil, true, g.builtinStatus(r.SyntaxLocation, r.Operator, 1)
		}
		g.io.streams.SetWriter(redirectDescriptor(r), g.io.streams.Writer(src))
		return nil, false, nil
	case "<&":
		src, _ := strconv.Atoi(staticWord(r.Target))
		if !g.io.streams.hasReader(src) {
			return nil, true, g.builtinStatus(r.SyntaxLocation, r.Operator, 1)
		}
		g.io.streams.SetReader(redirectDescriptor(r), g.io.streams.Reader(src))
		return nil, false, nil
	}
	var child *childRun
	var err error
	if r.Operator == "<" && field.ProcessSubstitution() == nil && field.Value != "/dev/null" && !strings.EqualFold(field.Value, "NUL") {
		child, err = g.beginRecorded(ctx, r.SyntaxLocation, r.Operator)
	} else {
		child, err = g.begin(r.SyntaxLocation, r.Operator)
	}
	if err != nil {
		return nil, false, err
	}
	child.producer = false
	if r.Operator == "<" {
		if subst := field.ProcessSubstitution(); subst != nil {
			if input, ok := subst.OpenReader(); ok {
				s.inputs = append(s.inputs, input)
				g.io.streams.SetReader(redirectDescriptor(r), input)
				child.settle(ctx, ordinaryResult(0), nil)
				return nil, false, nil
			}
		}
		input, err := s.input.OpenInput(ctx, InputDestination{Invocation: child.inv, Redirect: r, Target: field})
		if err != nil {
			if stop := g.redirectFailure(ctx, child, r.Operator, field.Value, err); stop != nil {
				return nil, true, stop
			}
			return nil, true, &descriptorOpenFailure{Status: g.state.control.Status(), Cause: err}
		}
		s.inputs = append(s.inputs, input)
		g.io.streams.SetReader(redirectDescriptor(r), borrowedInput{Reader: input})
		child.settle(ctx, ordinaryResult(0), nil)
		return nil, false, nil
	}
	var staged StagedOutput
	if subst := field.ProcessSubstitution(); subst != nil {
		if writer, ok := subst.OpenWriter(); ok {
			staged = NewProcessSubstitutionStagedOutput(writer)
		}
	}
	if staged == nil {
		var openErr error
		staged, openErr = s.output.Open(ctx, OutputDestination{Invocation: child.inv, Redirect: r, Target: field})
		if openErr != nil {
			if stop := g.redirectFailure(ctx, child, r.Operator, field.Value, openErr); stop != nil {
				return nil, true, stop
			}
			return nil, true, &descriptorOpenFailure{Status: g.state.control.Status(), Cause: openErr}
		}
	}
	errorStream := g.io.streams.Error
	switch r.Operator {
	case "&>", "&>>":
		g.io.streams.SetWriter(1, staged)
		g.io.streams.SetWriter(2, staged)
	default:
		g.io.streams.SetWriter(redirectDescriptor(r), staged)
	}
	return &stagedRedirect{frame: g, run: child, operator: r.Operator, target: field.Value, errorStream: errorStream, output: staged}, false, nil
}

func redirectDescriptor(r commandsurface.ProgramRedirect) int {
	if r.Descriptor >= 0 {
		return r.Descriptor
	}
	switch r.Operator {
	case "<", "<&", "<<", "<<-", "<<<":
		return 0
	default:
		return 1
	}
}

// redirectFailure records a refused open: owner stops latch, other failures
// leave an ordinary status 1 and the command unrun.
func (g *frame) redirectFailure(ctx context.Context, child *childRun, operator, target string, err error) error {
	result := ownerResult(err)
	if report := g.e.ports.Refusals; report != nil {
		report(g.io.streams.Error, RefusalReport{Operation: operator, Operand: target, Code: result.StopCode, Span: child.inv.Span, Cause: err})
	}
	if settleErr := g.settle(ctx, child, result, err); settleErr != nil {
		return settleErr
	}
	return nil
}

// preflight refuses, before the first statement, every reachable construct
// this evaluation cannot run: reserved function names and constructs whose
// owner port is absent. Nothing has been dispatched when it refuses.
func preflight(list []commandsurface.Statement, ports EvaluationPorts) error {
	var err error
	refuse := func(loc commandsurface.SyntaxLocation, expected string) {
		if err == nil {
			err = &EvaluationRefusal{Code: "COMMAND_SYNTAX_UNSUPPORTED", Node: loc, Expected: expected}
		}
	}
	var statements func([]commandsurface.Statement)
	var words func([]commandsurface.DeferredWord)
	var parts func([]commandsurface.WordNode)
	parts = func(items []commandsurface.WordNode) {
		for _, part := range items {
			switch p := part.(type) {
			case commandsurface.DoubleQuotedPart:
				parts(p.Parts)
			case commandsurface.SubstitutionPart:
				statements(p.Statements)
			case commandsurface.ParameterPart:
				words(p.Words())
			case commandsurface.ArithmeticPart:
				words(p.Words())
			case commandsurface.BracePart:
				words(p.Alternatives)
			}
		}
	}
	words = func(items []commandsurface.DeferredWord) {
		for _, w := range items {
			parts(w.Parts)
		}
	}
	var test func(commandsurface.TestNode)
	test = func(n commandsurface.TestNode) {
		switch t := n.(type) {
		case commandsurface.TestWord:
			words([]commandsurface.DeferredWord{t.Word})
		case commandsurface.UnaryPredicate:
			test(t.Operand)
		case commandsurface.BinaryPredicate:
			test(t.Left)
			test(t.Right)
		case commandsurface.GroupedPredicate:
			test(t.Expression)
		}
	}
	var statement func(commandsurface.Statement)
	statement = func(s commandsurface.Statement) {
		if s.Background && ports.Supervision == nil {
			refuse(s.SyntaxLocation, "trailing & with host job supervision")
		}
		for _, r := range s.Redirects {
			words([]commandsurface.DeferredWord{r.Target})
			if r.HereDocument != nil {
				words([]commandsurface.DeferredWord{*r.HereDocument})
			}
			switch r.Operator {
			case "<":
				if ports.Input == nil || r.Descriptor == 1 || r.Descriptor == 2 {
					refuse(r.SyntaxLocation, "< with an input owner on descriptor 0")
				}
			case ">", ">>", "&>", "&>>":
				if ports.Publication == nil || r.Descriptor == 0 {
					refuse(r.SyntaxLocation, "output redirection with a publication owner on descriptor 1 or 2")
				}
			}
		}
		switch c := s.Command.(type) {
		case commandsurface.SimpleCommand:
			for _, a := range c.Assignments {
				if a.Index != nil {
					words(commandsurface.ArithmeticPart{Expression: a.Index}.Words())
				}
				words([]commandsurface.DeferredWord{a.Value})
				words(a.Array)
			}
			words(c.Words)
			if len(c.Words) > 0 && staticWord(c.Words[0]) == "source" && ports.Sources == nil {
				refuse(c.SyntaxLocation, "source -- PATH with a source capture owner")
			}
		case commandsurface.EnvironmentDeclaration:
			for _, a := range c.Assignments {
				words([]commandsurface.DeferredWord{a.Value})
			}
			words(c.Words)
		case commandsurface.StatementGroup:
			statements(c.Statements)
		case commandsurface.BinaryCommand:
			statement(c.Left)
			statement(c.Right)
		case commandsurface.Conditional:
			for branch := &c; branch != nil; branch = branch.Else {
				statements(branch.Condition)
				statements(branch.Then)
			}
		case commandsurface.WhileLoop:
			statements(c.Condition)
			statements(c.Body)
		case commandsurface.ForLoop:
			words(c.Items)
			statements(c.Body)
		case commandsurface.CaseStatement:
			words([]commandsurface.DeferredWord{c.Word})
			for _, item := range c.Items {
				words(item.Patterns)
				statements(item.Body)
			}
		case commandsurface.FunctionDeclaration:
			if controlBuiltins[c.Name] || c.Name == "source" {
				refuse(c.SyntaxLocation, "NAME() { ...; } with a non-reserved name")
			}
			statement(c.Body)
		case commandsurface.TestExpression:
			test(c.Expression)
		case commandsurface.ArithmeticCommand:
			words(c.Words())
		case commandsurface.TrapCommand:
			statements(c.Statements)
		case commandsurface.TimedCommand:
			statements(c.Pipeline)
		}
	}
	statements = func(items []commandsurface.Statement) {
		for _, s := range items {
			statement(s)
		}
	}
	statements(list)
	return err
}

// staticWord returns a word made only of literal and quoted literal parts.
func staticWord(w commandsurface.DeferredWord) string {
	var b strings.Builder
	var literal func([]commandsurface.WordNode) bool
	literal = func(items []commandsurface.WordNode) bool {
		for _, part := range items {
			switch p := part.(type) {
			case commandsurface.LiteralPart:
				b.WriteString(p.Value)
			case commandsurface.SingleQuotedPart:
				b.WriteString(p.Value)
			case commandsurface.DoubleQuotedPart:
				if !literal(p.Parts) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	if !literal(w.Parts) {
		return ""
	}
	return b.String()
}

// redirections are the redirect owners for this frame: a directory frame that
// binds its own targets supplies them, so cd moves redirect resolution too.
func (g *frame) redirections() (InputRedirection, OutputPublication) {
	if frame, ok := g.state.dispatch.(FrameRedirections); ok {
		if input, output := frame.Redirections(); input != nil && output != nil {
			return input, output
		}
	}
	return g.e.ports.Input, g.e.ports.Publication
}
