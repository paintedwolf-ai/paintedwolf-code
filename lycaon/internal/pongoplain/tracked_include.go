package pongoplain

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/flosch/pongo2/v6"
)

const setProbePrefix = "\x00pongoplain-set-probe:"

var (
	errSetProbe   = errors.New("pongoplain: set probe")
	setProbeSeq   atomic.Uint64
	setProbeSlots sync.Map
)

type composedSetLoader struct {
	inner       pongo2.TemplateLoader
	set         *pongo2.TemplateSet
	activeLoads atomic.Int32
}

func (l *composedSetLoader) Abs(base, name string) string {
	if strings.HasPrefix(name, setProbePrefix) {
		return name
	}
	return l.inner.Abs(base, name)
}

func (l *composedSetLoader) Get(path string) (io.Reader, error) {
	if strings.HasPrefix(path, setProbePrefix) {
		if slot, ok := setProbeSlots.Load(path); ok {
			*(slot.(**composedSetLoader)) = l
		}
		return nil, errSetProbe
	}
	return l.inner.Get(path)
}

func (l *composedSetLoader) loadIncluded(baseFilename, rawTarget string) (string, *pongo2.Template, error) {
	includedFilename := rawTarget
	if baseFilename != "" && baseFilename != "<string>" {
		includedFilename = l.inner.Abs(baseFilename, rawTarget)
	}
	depth := l.activeLoads.Add(1)
	defer l.activeLoads.Add(-1)
	if int(depth) > MaxCompositionFiles {
		return includedFilename, nil, fmt.Errorf("%w: include nesting limit exceeded at %q", ErrComposition, includedFilename)
	}
	tpl, err := l.set.FromFile(includedFilename)
	return includedFilename, tpl, err
}

func resolveSetLoader(doc *pongo2.Parser, tok *pongo2.Token) (*composedSetLoader, string) {
	docErr := doc.Error("", tok)
	if docErr == nil || docErr.Template == nil {
		return nil, ""
	}
	probeKey := fmt.Sprintf("%s%d", setProbePrefix, setProbeSeq.Add(1))
	var captured *composedSetLoader
	setProbeSlots.Store(probeKey, &captured)
	defer setProbeSlots.Delete(probeKey)

	_, _, _ = (&pongo2.Error{
		Template: docErr.Template,
		Filename: probeKey,
		Line:     1,
	}).RawLine()
	return captured, docErr.Filename
}

func parserOrigError(p *pongo2.Parser, err error, tok *pongo2.Token) *pongo2.Error {
	var pErr *pongo2.Error
	if errors.As(err, &pErr) {
		if pErr.Line <= 0 && tok != nil {
			pErr.Token = tok
			pErr.Line = tok.Line
			pErr.Column = tok.Col
		}
		return pErr
	}
	out := p.Error(err.Error(), tok)
	out.OrigError = err
	return out
}

type trackedIncludeEmptyNode struct{}

func (node *trackedIncludeEmptyNode) Execute(ctx *pongo2.ExecutionContext, _ pongo2.TemplateWriter) *pongo2.Error {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	state.step(1)
	return nil
}

type trackedIncludeNode struct {
	tpl       *pongo2.Template
	filename  string
	only      bool
	withPairs []bindingPair
}

func (node *trackedIncludeNode) Execute(ctx *pongo2.ExecutionContext, writer pongo2.TemplateWriter) *pongo2.Error {
	state, stateErr := requireExecState(ctx)
	if stateErr != nil {
		return stateErr
	}
	state.includeDepth++
	defer func() { state.includeDepth-- }()
	if state.includeDepth > MaxCompositionDepth {
		panic(executionAbort{err: fmt.Errorf("%w: depth exceeds %d at %q", ErrComposition, MaxCompositionDepth, node.filename)})
	}
	state.includes++
	if state.includes > MaxCompositionFiles {
		panic(executionAbort{err: fmt.Errorf("%w: more than %d template includes", ErrComposition, MaxCompositionFiles)})
	}

	includeCtx := make(pongo2.Context)
	if !node.only {
		includeCtx.Update(ctx.Public)
		includeCtx.Update(ctx.Private)
	}
	// MaxCompositionFiles and the context entry limits bound the copy above, so
	// the include spends one step and its body spends its own.
	state.step(1)

	for _, pair := range node.withPairs {
		state.step(1)
		val, evalErr := pair.expr.Evaluate(ctx)
		if evalErr != nil {
			return evalErr
		}
		includeCtx[pair.key] = val
	}
	includeCtx[execStateKey] = state

	if execErr := node.tpl.ExecuteWriterUnbuffered(includeCtx, writer); execErr != nil {
		var pErr *pongo2.Error
		if errors.As(execErr, &pErr) {
			return pErr
		}
		return ctx.OrigError(execErr, nil)
	}
	return nil
}

func parseTrackedIncludeTag(doc *pongo2.Parser, start *pongo2.Token, arguments *pongo2.Parser) (pongo2.INodeTag, *pongo2.Error) {
	filenameToken := arguments.MatchType(pongo2.TokenString)
	if filenameToken == nil || filenameToken.Val == "" {
		return nil, parserOrigError(arguments, fmt.Errorf("%w: include dependency must be a string literal", ErrComposition), start)
	}
	ifExists := arguments.Match(pongo2.TokenIdentifier, "if_exists") != nil

	includeNode := &trackedIncludeNode{}
	if arguments.Match(pongo2.TokenIdentifier, "only") != nil {
		includeNode.only = true
	} else if arguments.Match(pongo2.TokenIdentifier, "with") != nil {
		if arguments.Remaining() == 0 {
			return nil, arguments.Error("Expected at least one key=expr pair after 'with'.", nil)
		}
		for arguments.Remaining() > 0 {
			keyToken := arguments.MatchType(pongo2.TokenIdentifier)
			if keyToken == nil {
				return nil, arguments.Error("Expected an identifier", nil)
			}
			if arguments.Match(pongo2.TokenSymbol, "=") == nil {
				return nil, arguments.Error("Expected '='.", nil)
			}
			valueExpr, err := arguments.ParseExpression()
			if err != nil {
				return nil, err
			}
			includeNode.withPairs = append(includeNode.withPairs, bindingPair{key: keyToken.Val, expr: valueExpr})
			if arguments.Match(pongo2.TokenIdentifier, "only") != nil {
				includeNode.only = true
				break
			}
		}
	}
	if arguments.Remaining() > 0 {
		return nil, arguments.Error("Malformed 'include'-tag arguments.", nil)
	}

	setLoader, baseFilename := resolveSetLoader(doc, start)
	if setLoader == nil || setLoader.set == nil {
		return nil, parserOrigError(doc, fmt.Errorf("%w: template set missing composition policy", ErrComposition), start)
	}
	includedFilename, includedTpl, loadErr := setLoader.loadIncluded(baseFilename, filenameToken.Val)
	if loadErr != nil {
		var pErr *pongo2.Error
		if ifExists && errors.As(loadErr, &pErr) && pErr.Sender == "fromfile" {
			return &trackedIncludeEmptyNode{}, nil
		}
		return nil, parserOrigError(doc, loadErr, filenameToken)
	}
	includeNode.filename = includedFilename
	includeNode.tpl = includedTpl
	return includeNode, nil
}
