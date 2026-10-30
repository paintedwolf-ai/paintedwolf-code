package commandprogram

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// WordExpander implements Expander for one program. It reads only the scope it
// is given, dispatches substitutions through the evaluator's port and reads
// directories through the logical-path view; it never assigns variables,
// records statuses or reparses data. Glob matches and traversal visits are
// charged cumulatively to the program, and live intermediate buffers to
// expansion_memory_bytes and the program's working memory.
type WordExpander struct {
	substitutions Substitution
	files         commandsurface.GlobFiles
	memory        *toolcontract.ProgramMemory
	limits        expansionLimits

	mu          sync.Mutex
	live        int64
	globMu      sync.Mutex
	globMatches int64
	globVisits  int64
}

var _ Expander = (*WordExpander)(nil)

// ExpanderPorts are a program's expansion dependencies. Memory is required
// unless LocalBoundsOnly explicitly selects pure fixture bounds.
type ExpanderPorts struct {
	Substitutions   Substitution
	Files           commandsurface.GlobFiles
	Memory          *toolcontract.ProgramMemory
	LocalBoundsOnly bool
}

func NewWordExpander(ports ExpanderPorts) (*WordExpander, error) {
	if ports.Substitutions == nil || ports.Files == nil {
		return nil, errors.New("word expansion requires substitution and logical-path ports")
	}
	if ports.Memory == nil && !ports.LocalBoundsOnly {
		return nil, errors.New("word expansion requires aggregate memory admission")
	}
	limits, err := catalogExpansionLimits()
	if err != nil {
		return nil, err
	}
	return &WordExpander{substitutions: ports.Substitutions, files: ports.Files, memory: ports.Memory, limits: limits}, nil
}

func (e *WordExpander) ExpandFields(ctx context.Context, scope ExpansionScope, word commandsurface.DeferredWord) (Fields, error) {
	run, err := e.begin(ctx, scope, word, modeFields)
	if err != nil {
		return nil, err
	}
	defer run.close()
	//nolint:contextcheck // Expansion retains the operation context; substitution derives its child from it.
	if err := run.word(word.Parts); err != nil {
		return nil, err
	}
	if err := run.assembly.finish(); err != nil {
		return nil, err
	}
	return run.fields()
}

func (e *WordExpander) ExpandScalar(ctx context.Context, scope ExpansionScope, word commandsurface.DeferredWord) (string, error) {
	run, err := e.begin(ctx, scope, word, modeScalar)
	if err != nil {
		return "", err
	}
	defer run.close()
	//nolint:contextcheck // Expansion retains the operation context; substitution derives its child from it.
	if err := run.word(word.Parts); err != nil {
		return "", err
	}
	return run.join(run.assembly.current, e.limits.valueBytes, "variable_value_bytes")
}

func (e *WordExpander) ExpandPattern(ctx context.Context, scope ExpansionScope, word commandsurface.DeferredWord) (PredicateValue, error) {
	run, err := e.begin(ctx, scope, word, modePattern)
	if err != nil {
		return nil, err
	}
	defer run.close()
	//nolint:contextcheck // Expansion retains the operation context; substitution derives its child from it.
	if err := run.word(word.Parts); err != nil {
		return nil, err
	}
	var size int64
	value := make(PredicateValue, 0, len(run.assembly.current))
	for _, piece := range run.assembly.current {
		size += int64(len(piece.text))
		value = append(value, PredicateFragment{Value: piece.text, Quoted: piece.quoted})
	}
	if size > e.limits.argumentBytes {
		return nil, e.limits.exceeded("argument_bytes", size, word.Span)
	}
	return value, nil
}

func (e *WordExpander) ExpandDocument(ctx context.Context, scope ExpansionScope, redirect commandsurface.ProgramRedirect) (string, error) {
	if redirect.Operator != "<<" && redirect.Operator != "<<-" {
		return "", invalidValue(redirect.Span, "a here-document redirect")
	}
	if redirect.HereDocument == nil {
		return "", nil
	}
	body := *redirect.HereDocument
	run, err := e.begin(ctx, scope, body, modeScalar)
	if err != nil {
		return "", err
	}
	defer run.close()
	run.document = &documentText{literal: delimiterQuoted(redirect.Target), stripTabs: redirect.Operator == "<<-", lineStart: true}
	//nolint:contextcheck // Expansion retains the operation context; substitution derives its child from it.
	if err := run.word(body.Parts); err != nil {
		return "", err
	}
	return run.join(run.assembly.current, e.limits.documentBytes, "here_document_bytes")
}

// delimiterQuoted reports any quoting in a here-document delimiter, which makes
// the body literal.
func delimiterQuoted(target commandsurface.DeferredWord) bool {
	for _, part := range target.Parts {
		switch part := part.(type) {
		case commandsurface.LiteralPart:
			if strings.Contains(part.Value, `\`) {
				return true
			}
		case commandsurface.SingleQuotedPart, commandsurface.DoubleQuotedPart:
			return true
		}
	}
	return false
}

type expansionMode uint8

const (
	modeFields expansionMode = iota
	modeScalar
	modePattern
)

func (e *WordExpander) begin(ctx context.Context, scope ExpansionScope, word commandsurface.DeferredWord, mode expansionMode) (*expansionRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scope == nil {
		return nil, errors.New("word expansion requires an invocation scope")
	}
	run := &expansionRun{ctx: ctx, expander: e, scope: scope, span: word.Span, mode: mode}
	run.assembly = fieldAssembly{limits: &e.limits, span: word.Span, charge: run.charge}
	return run, nil
}

// charge admits n bytes of live intermediate storage for one run.
func (r *expansionRun) charge(n int64) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	e := r.expander
	e.mu.Lock()
	if n > e.limits.memory-e.live {
		observed := e.live + n
		e.mu.Unlock()
		return e.limits.exceeded("expansion_memory_bytes", observed, r.span)
	}
	e.live += n
	e.mu.Unlock()
	r.charged += n
	if e.memory != nil {
		release, err := e.memory.Reserve(r.ctx, n)
		if err != nil {
			return err
		}
		r.releases = append(r.releases, release)
	}
	return nil
}

func (r *expansionRun) close() {
	for i := len(r.releases) - 1; i >= 0; i-- {
		r.releases[i]()
	}
	r.expander.mu.Lock()
	r.expander.live -= r.charged
	r.expander.mu.Unlock()
}
