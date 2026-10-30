package search

import (
	"errors"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// findRun is one bound find invocation.
type findRun struct {
	inv        *invocation
	walker     *walker
	expr       *findNode
	actions    bool
	depthFirst bool
	maxDepth   int
	minDepth   int
	now        time.Time
	// followAll is -L, followArgs -H; sameDevice is -x/-xdev/-mount.
	followAll, followArgs, sameDevice bool
	device                            uint64
	ancestors                         map[[2]uint64]string
	// parser lets a primary read a following operand (BSD -depth N).
	parser   *findParser
	pruned   bool
	batches  []*execBatch
	children int64
}

// execBatch accumulates one -exec ... {} + primary's paths.
type execBatch struct {
	words commandprogram.Fields
	span  toolcontract.SourceSpan
	paths commandprogram.Fields
	bytes int64
}

func unavailable(form, reason string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{"command": form, "detail": reason}}
}

// runFind walks each starting point in byte order, pre-order unless -delete
// asks for depth-first, and evaluates the expression at every admitted depth.
// Without an action the expression is ( EXPR ) -print. Links are entries,
// never followed.
func runFind(inv *invocation) error {
	inv.failure = 1
	r := &findRun{inv: inv, walker: newWalker(inv), maxDepth: -1, now: time.Now()}
	r.walker.lazy = true
	defer r.walker.report()
	if inv.owners.Now != nil {
		r.now = inv.owners.Now()
	}
	items := inv.argv.Items
	// BSD and GNU global options precede the starting points.
	for len(items) > 0 && items[0].Option != nil && r.leadingOption(items[0].Option.Key) {
		items = items[1:]
	}
	var starts []string
	for len(items) > 0 && items[0].Operand != nil && !expressionWord(items[0].Operand.Value) {
		starts = append(starts, items[0].Operand.Value)
		items = items[1:]
	}
	if len(starts) == 0 {
		starts = []string{"."}
	}
	if len(items) > 0 {
		p := &findParser{run: r, items: items}
		r.parser = p
		expr, err := p.parseOr()
		if errors.Is(err, errFindAbort) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.pos < len(p.items) {
			word, _ := p.word()
			return inv.usage(word, "an operator or primary; unbalanced )")
		}
		r.expr = expr
	}
	if !r.actions {
		print := &findNode{op: "primary", test: func(r *findRun, e entry) (bool, error) { return true, r.inv.out.text(e.display, "\n") }}
		if r.expr == nil {
			r.expr = print
		} else {
			r.expr = &findNode{op: "and", left: r.expr, right: print}
		}
	}
	for _, start := range starts {
		e, err := r.walker.operand(start, r.followAll || r.followArgs)
		if err != nil {
			if reason, ok := ordinary(err); ok {
				inv.fail(false, "'%s': %s", start, reason)
				continue
			}
			return err
		}
		r.device, r.ancestors = e.stat.Device, map[[2]uint64]string{}
		if err := r.visit(e); err != nil {
			return err
		}
	}
	for _, batch := range r.batches {
		if err := r.flush(batch); err != nil {
			return err
		}
	}
	return nil
}

// leadingOption applies one global option written before the starting
// points and reports whether it was one.
func (r *findRun) leadingOption(key string) bool {
	switch key {
	case "-E", "-s", "-P":
	case "-x":
		r.sameDevice = true
	case "-H":
		r.followArgs, r.followAll = true, false
	case "-L":
		r.followAll, r.followArgs = true, false
	case "-d":
		r.depthFirst = true
	default:
		return false
	}
	return true
}

// expressionWord reports an operator word that ends the starting points.
func expressionWord(word string) bool { return word == "!" || word == "(" || word == ")" }

func (r *findRun) visit(e entry) error {
	if err := r.inv.ctx.Err(); err != nil {
		return err
	}
	if e.dir() || e.link {
		var err error
		e, err = r.walker.inspect(e, r.followAll)
		if err != nil {
			if _, ok := ordinary(err); ok {
				return nil
			}
			return err
		}
	}
	inRange := e.depth >= r.minDepth && (r.maxDepth < 0 || e.depth <= r.maxDepth)
	descend := e.dir() && !e.link && (r.maxDepth < 0 || e.depth < r.maxDepth) && (!r.sameDevice || e.stat.Device == r.device)
	if descend && (r.followAll || r.followArgs) {
		if id, ok := identity(e.stat); ok {
			if first, loop := r.ancestors[id]; loop {
				r.inv.fail(false, "File system loop detected; '%s' is part of the same file system loop as '%s'.", e.display, first)
				descend = false
			} else {
				r.ancestors[id] = e.display
				defer delete(r.ancestors, id)
			}
		}
	}
	if !r.depthFirst {
		r.pruned = false
		if inRange {
			if _, err := r.expr.eval(r, e); err != nil {
				return err
			}
		}
		if r.pruned {
			return nil
		}
	}
	if descend {
		children, err := r.walker.children(e, r.followAll)
		if err != nil {
			reason, ok := ordinary(err)
			if !ok {
				return err
			}
			r.inv.fail(false, "'%s': %s", e.display, reason)
		}
		for _, child := range children {
			if err := r.visit(child); err != nil {
				return err
			}
		}
	}
	if r.depthFirst && inRange {
		_, err := r.expr.eval(r, e)
		return err
	}
	return nil
}

// execPrimary binds -exec CMD ARGS ; (once per entry, true on status zero)
// or -exec CMD ARGS {} + (serial batches within argv_bytes; always true).
// Each child is an ordinary program child: the evaluator's dispatcher
// resolves, admits and records it as it does any reached command.
func (r *findRun) execPrimary(option commandprogram.BoundOption) (*findNode, error) {
	inv := r.inv
	if inv.dispatch == nil {
		return nil, unavailable("find -exec", "no program dispatcher is attached to this invocation")
	}
	words := option.Values
	if option.Terminator == "+" {
		head := words[:len(words)-1]
		for _, word := range head {
			if strings.Contains(word.Value, "{}") {
				return nil, inv.usage(word.Value, "one {} immediately before + in -exec ... {} +")
			}
		}
		batch := &execBatch{words: head, span: words[len(words)-1].Span}
		r.batches = append(r.batches, batch)
		return &findNode{op: "primary", test: func(r *findRun, e entry) (bool, error) {
			return true, r.batch(batch, e.display)
		}}, nil
	}
	return &findNode{op: "primary", test: func(r *findRun, e entry) (bool, error) {
		argv := make(commandprogram.Fields, len(words))
		for i, word := range words {
			argv[i] = commandprogram.Field{Value: strings.ReplaceAll(word.Value, "{}", e.display), Span: word.Span}
		}
		result, err := r.dispatch(argv)
		if err != nil {
			return false, err
		}
		return result.Status == 0, nil
	}}, nil
}

func (r *findRun) batch(b *execBatch, path string) error {
	maximum := r.inv.owners.limit("argv_bytes")
	if b.bytes == 0 {
		for _, word := range b.words {
			b.bytes += int64(len(word.Value)) + 1
		}
	}
	size := int64(len(path)) + 1
	if b.bytes+size > maximum && len(b.paths) > 0 {
		if err := r.flush(b); err != nil {
			return err
		}
	}
	b.paths = append(b.paths, commandprogram.Field{Value: path, Span: b.span})
	b.bytes += size
	return nil
}

func (r *findRun) flush(b *execBatch) error {
	if len(b.paths) == 0 {
		return nil
	}
	argv := append(append(commandprogram.Fields{}, b.words...), b.paths...)
	b.paths, b.bytes = nil, 0
	result, err := r.dispatch(argv)
	if err != nil {
		return err
	}
	if result.Status != 0 {
		r.inv.status = max(r.inv.status, 1)
	}
	return nil
}

// dispatch hands one -exec child to the evaluator frame as an ordinary
// program child of this find execution. An ordinary child failure is a
// false test; a refusal, interruption or owner stop ends find with it.
func (r *findRun) dispatch(argv commandprogram.Fields) (toolcontract.ProducerResult, error) {
	inv := r.inv
	r.children++
	if maximum := inv.owners.limit("child_executions"); maximum > 0 && r.children > maximum {
		return toolcontract.ProducerResult{}, limitExceeded("child_executions", maximum, r.children)
	}
	spec, native, err := commandprogram.ResolveCommand(argv)
	if err != nil {
		return toolcontract.ProducerResult{}, err
	}
	operation := "external"
	if native {
		operation = spec.Name
	}
	statement := inv.request.Statement
	request := commandprogram.OperationRequest{
		Invocation:  commandprogram.NextExecution(inv.request.Invocation, string(statement.ID), statement.Span),
		OperationID: operation, Statement: statement, Argv: argv, Environment: inv.request.Environment,
	}
	if native && len(spec.Effects) == 1 {
		request.EffectMode = spec.Effects[0].Mode
	}
	streams := commandprogram.Streams{Input: commandprogram.NullInput(), Output: inv.out, Error: inv.stderr}
	var result toolcontract.ProducerResult
	if native && spec.Name != "external" {
		result, err = inv.dispatch.Invoke(inv.ctx, request, streams)
	} else {
		result, err = inv.dispatch.Launch(inv.ctx, request, streams)
	}
	if invalid := result.Validate(); invalid != nil {
		return result, errors.Join(err, invalid)
	}
	switch result.Terminal {
	case toolcontract.ProducerRefused, toolcontract.ProducerInterrupted:
		if err == nil {
			err = &toolrejection.ToolReject{Code: result.StopCode, Data: map[string]any{"command": spec.Name}}
		}
		return result, err
	}
	return result, nil
}

// remove routes one entry through the recoverable removal owner. The
// starting point "." is never removed. A non-empty directory or missing
// entry is an ordinary failure; other refusals stop find.
func (r *findRun) remove(e entry) (bool, error) {
	inv := r.inv
	if e.display == "." || e.display == "./" {
		return true, nil
	}
	target, err := inv.paths.Bind(inv.ctx, e.display, true)
	if err == nil {
		err = inv.owners.Remover.RemoveRecoverable(inv.ctx, inv.tc, target)
	}
	if err == nil {
		inv.effects = toolcontract.EffectCompleted
		return true, nil
	}
	reason, ok := ordinary(err)
	if reject := toolrejection.AsToolReject(err); reject != nil {
		switch reject.Code {
		case "DELETE_NOT_EMPTY":
			reason, ok = "Directory not empty", true
		case "DELETE_NOT_FOUND":
			reason, ok = "No such file or directory", true
		}
	}
	if !ok {
		return false, err
	}
	inv.fail(false, "cannot delete '%s': %s", e.display, reason)
	return false, nil
}
