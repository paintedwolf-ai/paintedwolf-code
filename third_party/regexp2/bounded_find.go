package regexp2

import (
	"context"
	"fmt"
	"regexp/syntax"

	"github.com/dlclark/regexp2/v2/internal/budget"
	enginesyntax "github.com/dlclark/regexp2/v2/syntax"
	posixsyntax "github.com/dlclark/regexp2/v2/syntax/posix"
)

// Host markers a validated tree may carry for assertions the standard syntax
// tree cannot spell (see enginesyntax.FromMarkedSyntax).
const (
	MarkerBackref         = enginesyntax.MarkerBackref
	MarkerWordStart       = enginesyntax.MarkerWordStart
	MarkerWordEnd         = enginesyntax.MarkerWordEnd
	MarkerNotAfterWord    = enginesyntax.MarkerNotAfterWord
	MarkerNotBeforeWord   = enginesyntax.MarkerNotBeforeWord
	MarkerWordBoundary    = enginesyntax.MarkerWordBoundary
	MarkerNotWordBoundary = enginesyntax.MarkerNotWordBoundary
	MaxMarkedGroup        = enginesyntax.MaxMarkedGroup
)

// IsMarker reports whether r is reserved for host markers.
func IsMarker(r rune) bool { return enginesyntax.IsMarker(r) }

// MarkedInputBound is the repetition bound a compile-once marked program
// lowers with. It must exceed every scalar count the program will match.
const MarkedInputBound = 1 << 30

// ParseSyntaxBounded runs the maintained standard-grammar parser under a
// meter and returns its detached tree for host validation and lowering.
func ParseSyntaxBounded(ctx context.Context, source string, flags syntax.Flags, limits SyntaxBudget) (tree *syntax.Regexp, err error) {
	defer recoverBudget(&err)
	m := budget.New(ctx, limits)
	defer m.Close()
	return posixsyntax.ParseMetered(source, flags, m)
}

// BoundedProgram is one metered compilation that many bounded sessions run.
// It retains no runner, pool or generated engine.
type BoundedProgram struct {
	code    *enginesyntax.Code
	options RegexOptions
}

// CompileMarkedBounded compiles a host-validated standard tree with numbered
// captures, references and host markers under a meter.
func CompileMarkedBounded(ctx context.Context, source *syntax.Regexp, limits SyntaxBudget) (program *BoundedProgram, err error) {
	return compileSyntaxBounded(ctx, source, limits, true, false)
}

// CompileSyntaxBounded preserves standard-tree captures, flags and literal
// scalars. Unlike the host-marked entry, it never interprets private scalars.
func CompileSyntaxBounded(ctx context.Context, source *syntax.Regexp, limits SyntaxBudget) (*BoundedProgram, error) {
	return compileSyntaxBounded(ctx, source, limits, false, false)
}

// CompileWordSyntaxBounded wraps a literal-preserving standard tree in ASCII
// word exclusions. Pattern scalars cannot acquire host-marker authority.
func CompileWordSyntaxBounded(ctx context.Context, source *syntax.Regexp, limits SyntaxBudget) (*BoundedProgram, error) {
	return compileSyntaxBounded(ctx, source, limits, false, true)
}

func compileSyntaxBounded(ctx context.Context, source *syntax.Regexp, limits SyntaxBudget, marked, word bool) (program *BoundedProgram, err error) {
	defer recoverBudget(&err)
	m := budget.New(ctx, limits)
	defer m.Close()
	var tree *enginesyntax.RegexTree
	if marked {
		tree = enginesyntax.FromMarkedSyntax(source, m, MarkedInputBound)
	} else {
		tree = enginesyntax.FromCapturingSyntax(source, m, MarkedInputBound, word)
	}
	code, err := enginesyntax.WriteMetered(tree, m)
	if err != nil {
		return nil, err
	}
	return &BoundedProgram{code: code}, nil
}

// CompilePatternTextBounded compiles text in this engine's own grammar. The
// caller validates the text against its declared grammar first; this entry
// point is not a qualification of any wider syntax. Parsing is bounded by the
// caller's pattern size ceiling; code generation is metered.
func CompilePatternTextBounded(ctx context.Context, pattern string, options RegexOptions, limits SyntaxBudget) (program *BoundedProgram, err error) {
	defer recoverBudget(&err)
	m := budget.New(ctx, limits)
	defer m.Close()
	m.Step(int64(len(pattern)))
	tree, err := enginesyntax.Parse(pattern, enginesyntax.ParseOptions{RegexOptions: enginesyntax.RegexOptions(options)})
	if err != nil {
		return nil, err
	}
	code, err := enginesyntax.WriteMetered(tree, m)
	if err != nil {
		return nil, err
	}
	return &BoundedProgram{code: code, options: options}, nil
}

// BoundedSession runs a program against records under one meter, for example
// every search of one line. Close releases its admissions.
type BoundedSession struct {
	meter  *budget.Meter
	runner *Runner
}

// Session opens a metered run. Steps and allocation accumulate across
// every Find until Close.
func (p *BoundedProgram) Session(ctx context.Context, limits SyntaxBudget) (session *BoundedSession, err error) {
	defer recoverBudget(&err)
	m := budget.New(ctx, limits)
	re := &Regexp{code: p.code, options: p.options, caps: p.code.Caps, capsize: p.code.Capsize, MatchTimeout: DefaultMatchTimeout, optimizations: OptimizationOptions{MaxBacktrackingStackSize: -1}}
	return &BoundedSession{meter: m, runner: &Runner{re: re, code: p.code, budget: m}}, nil
}

// Charge admits host work against the session's step ceiling.
func (s *BoundedSession) Charge(steps int64) (err error) {
	defer recoverBudget(&err)
	s.meter.Step(steps)
	return nil
}

// ReserveMemory admits host-side record decoding before allocation.
func (s *BoundedSession) ReserveMemory(bytes int64) (err error) {
	defer recoverBudget(&err)
	s.meter.Alloc(bytes)
	return nil
}

// Steps reports the work this session admitted.
func (s *BoundedSession) Steps() int64 { return s.meter.Steps() }

// Close releases the session's admissions.
func (s *BoundedSession) Close() { s.meter.Close() }

// Find returns the first match at or after start as scalar offsets: group g
// spans groups[2g:2g+2], -1 for an unset group. Without longest the match is
// the backtracking engine's leftmost-first one. With longest each start
// explores every path and returns its farthest group-0 end, which is the
// POSIX leftmost-longest match with POSIX subexpression offsets.
func (s *BoundedSession) Find(text []rune, start int, longest bool) (groups []int, err error) {
	defer recoverBudget(&err)
	if start < 0 || start > len(text) {
		return nil, fmt.Errorf("bounded find start %d outside %d scalars", start, len(text))
	}
	r := s.runner
	r.longest = longest
	r.Runtext, r.Runtextstart, r.Runtextend = text, start, len(text)
	r.timeout, r.ignoreTimeout = DefaultMatchTimeout, true
	r.initMatch(nil)
	defer func() { r.runmatch = nil }()
	for pos := start; pos <= len(text); pos++ {
		s.meter.Step(1)
		r.Runtextpos = pos
		r.best = nil
		if err := executeDefault(r); err != nil {
			return nil, err
		}
		if longest && r.best != nil {
			return r.best, nil
		}
		if !longest && r.runmatch.matchcount[0] > 0 {
			return r.captureVector(), nil
		}
		r.Runtrackpos = len(r.runtrack)
		r.Runstackpos = len(r.runstack)
		r.runcrawlpos = len(r.runcrawl)
	}
	s.meter.Check()
	return nil, nil
}

// captureVector reads the current last capture of every group as scalar
// offsets, -1 for an unset group.
func (r *Runner) captureVector() []int {
	m := r.runmatch
	code := r.code
	top := code.Capsize
	if code.Caps != nil {
		top = 0
		for group := range code.Caps {
			top = max(top, group+1)
		}
	}
	r.budget.Alloc(int64(top) * 16)
	out := make([]int, 2*top)
	for group := range top {
		out[2*group], out[2*group+1] = -1, -1
		index := group
		if code.Caps != nil {
			mapped, ok := code.Caps[group]
			if !ok {
				continue
			}
			index = mapped
		}
		if count := m.matchcount[index]; count > 0 {
			begin, length := m.matches[index][2*count-2], m.matches[index][2*count-1]
			out[2*group], out[2*group+1] = begin, begin+length
		}
	}
	return out
}

// posixPrefers orders two complete matches from one start: the farther
// overall end, then group by group a set group over an unset one, the
// earlier start and the farther end.
func posixPrefers(candidate, best []int) bool {
	if candidate[1] != best[1] {
		return candidate[1] > best[1]
	}
	for i := 2; i+1 < len(candidate); i += 2 {
		switch {
		case candidate[i] == best[i] && candidate[i+1] == best[i+1]:
			continue
		case best[i] < 0:
			return true
		case candidate[i] < 0:
			return false
		case candidate[i] != best[i]:
			return candidate[i] < best[i]
		}
		return candidate[i+1] > best[i+1]
	}
	return false
}

func recoverBudget(err *error) {
	if recovered := recover(); recovered != nil {
		if stopped, ok := recovered.(budget.Abort); ok {
			*err = stopped.Err
			return
		}
		panic(recovered)
	}
}
