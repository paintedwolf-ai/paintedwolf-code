package commandprogram

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// redirected binds a statement's descriptors left to right, runs it, then
// publishes every staged output only when all producers under it were typed
// eligible and no host stop latched; otherwise staged bytes are abandoned.
func (f *frame) redirected(ctx context.Context, s commandsurface.Statement, run func(*frame) (signal, error)) (signal, error) {
	if len(s.Redirects) == 0 {
		sig, err := run(f)
		if procErr := f.procSubst.finish(ctx, f, err); procErr != nil && err == nil {
			err = procErr
		}
		return sig, err
	}
	g := *f
	g.publish = newEligibility()
	var staged []*stagedRedirect
	var openedInputs []io.Closer
	closeInputs := func() {
		for _, in := range openedInputs {
			_ = in.Close()
		}
		openedInputs = nil
	}
	closeProcSubstOutputs := func() {
		for _, out := range staged {
			if ps, ok := out.output.(*procSubstStagedOutput); ok {
				_ = ps.closeOnce()
			}
		}
	}
	settleStaged := func(publish bool) error {
		var first error
		for _, out := range staged {
			if err := out.finish(ctx, publish && first == nil); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	for _, r := range s.Redirects {
		out, in, failed, err := g.bind(ctx, r)
		if out != nil {
			staged = append(staged, out)
		}
		if in != nil {
			openedInputs = append(openedInputs, in)
		}
		if err != nil || failed {
			closeInputs()
			closeProcSubstOutputs()
			_ = g.procSubst.finish(ctx, &g, err)
			_ = settleStaged(false)
			return signal{}, err
		}
	}
	sig, err := run(&g)
	closeInputs()
	closeProcSubstOutputs()
	if procErr := g.procSubst.finish(ctx, &g, err); procErr != nil && err == nil {
		err = procErr
	}
	_, stopped := f.state.latch.Stop()
	eligible := err == nil && !stopped && g.publish.eligible()
	f.publish.mark(eligible)
	if publishErr := settleStaged(eligible); publishErr != nil && err == nil {
		err = publishErr
	}
	return sig, err
}

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
		o.run.settle(toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err)
		return nil
	}
	published, err := o.output.Publish(ctx)
	if err != nil {
		result := ownerResult(err)
		writeRedirectRefusal(o.errorStream, o.operator, o.target, result.StopCode)
		return o.frame.settle(o.run, result, err)
	}
	certainty := toolcontract.EffectCompleted
	if published.PublishedRevision == "" {
		certainty = toolcontract.NoEffect
	}
	o.run.settle(toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: certainty, Evidence: toolcontract.OperationEvidence{OwnerRef: published.PublishedRevision}}, nil)
	return nil
}

// bind applies one redirect to g.streams. failed reports an ordinary failure
// (for example a failed target substitution or a refused open) after which the
// command is not run; err reports a host stop.
func (g *frame) bind(ctx context.Context, r commandsurface.ProgramRedirect) (*stagedRedirect, io.Closer, bool, error) {
	switch r.Operator {
	case ">&":
		src, _ := strconv.Atoi(staticWord(r.Target))
		if !g.streams.hasWriter(src) {
			return nil, nil, true, g.builtinStatus(r.SyntaxLocation, r.Operator, 1)
		}
		g.streams.SetWriter(redirectDescriptor(r), g.streams.Writer(src))
		return nil, nil, false, nil
	case "<&":
		src, _ := strconv.Atoi(staticWord(r.Target))
		if !g.streams.hasReader(src) {
			return nil, nil, true, g.builtinStatus(r.SyntaxLocation, r.Operator, 1)
		}
		g.streams.SetReader(redirectDescriptor(r), g.streams.Reader(src))
		return nil, nil, false, nil
	case "<<", "<<-":
		out, failed, err := g.hereDocument(ctx, r)
		return out, nil, failed, err
	}
	var field Field
	prevented, err := g.expanding(r.SyntaxLocation, func(scope *frame) error {
		var err error
		field, err = g.e.expander.ExpandScalarField(ctx, scope, r.Target)
		return err
	})
	if prevented || err != nil {
		return nil, nil, true, err
	}
	target := field.Value
	child, err := g.beginRecorded(ctx, r.SyntaxLocation, r.Operator)
	if err != nil {
		return nil, nil, false, err
	}
	child.producer = false
	if r.Operator == "<" {
		if subst := field.ProcessSubstitution(); subst != nil {
			if input, ok := subst.OpenReader(); ok {
				g.streams.SetReader(redirectDescriptor(r), input)
				child.settle(ordinaryResult(0), nil)
				return nil, input, false, nil
			}
		}
		inputs, _ := g.redirections()
		input, err := inputs.OpenInput(ctx, InputDestination{Invocation: child.inv, Redirect: r, Target: field})
		if err != nil {
			return nil, nil, true, g.redirectFailure(child, r.Operator, target, err)
		}
		g.streams.SetReader(redirectDescriptor(r), input)
		child.settle(ordinaryResult(0), nil)
		return nil, input, false, nil
	}
	var staged StagedOutput
	if subst := field.ProcessSubstitution(); subst != nil {
		if writer, ok := subst.OpenWriter(); ok {
			staged = NewProcessSubstitutionStagedOutput(writer)
		}
	}
	if staged == nil {
		_, outputs := g.redirections()
		var openErr error
		staged, openErr = outputs.Open(ctx, OutputDestination{Invocation: child.inv, Redirect: r, Target: field})
		if openErr != nil {
			return nil, nil, true, g.redirectFailure(child, r.Operator, target, openErr)
		}
	}
	errorStream := g.streams.Error
	switch r.Operator {
	case "&>", "&>>":
		g.streams.SetWriter(1, staged)
		g.streams.SetWriter(2, staged)
	default:
		g.streams.SetWriter(redirectDescriptor(r), staged)
	}
	return &stagedRedirect{frame: g, run: child, operator: r.Operator, target: target, errorStream: errorStream, output: staged}, nil, false, nil
}

func redirectDescriptor(r commandsurface.ProgramRedirect) int {
	if r.Descriptor >= 0 {
		return r.Descriptor
	}
	switch r.Operator {
	case "<", "<&", "<<", "<<-":
		return 0
	default:
		return 1
	}
}

// redirectFailure records a refused open: owner stops latch, other failures
// leave an ordinary status 1 and the command unrun.
func (g *frame) redirectFailure(child *childRun, operator, target string, err error) error {
	result := ownerResult(err)
	writeRedirectRefusal(g.streams.Error, operator, target, result.StopCode)
	if settleErr := g.settle(child, result, err); settleErr != nil {
		return settleErr
	}
	return nil
}

func writeRedirectRefusal(w io.Writer, operator, target, code string) {
	if w == nil || code == "" {
		return
	}
	detail := "not supported here"
	switch code {
	case "SURVEY_PATH_ESCAPE":
		detail = "path is outside the project root; use a relative path inside the workspace"
	case "COMMAND_REVISION_CONFLICT":
		detail = "destination changed since capture; re-read before writing"
	}
	_, _ = fmt.Fprintf(w, "redirect %s: %s: %s [%s]\n", operator, target, detail, code)
}

// hereDocument expands the body once through the expander's document rules
// (bounded by here_document_bytes) and supplies it as stdin.
func (g *frame) hereDocument(ctx context.Context, r commandsurface.ProgramRedirect) (*stagedRedirect, bool, error) {
	var body string
	prevented, err := g.expanding(r.SyntaxLocation, func(scope *frame) error {
		var err error
		body, err = g.e.expander.ExpandDocument(ctx, scope, r)
		return err
	})
	if prevented || err != nil {
		return nil, true, err
	}
	g.streams.SetReader(redirectDescriptor(r), io.NopCloser(strings.NewReader(body)))
	return nil, false, nil
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
