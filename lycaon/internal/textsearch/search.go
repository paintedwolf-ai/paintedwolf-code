// Package textsearch is the shared typed line-search operation behind the
// structured grep tool and the grep/rg/find command frontends. It owns the
// pattern dialects, their bounded engines and the line scan; callers own
// traversal, reads and presentation.
//
// Dialects and engines:
//   - Fixed: exact bytes, leftmost then longest across patterns.
//   - Basic and Extended: GNU BRE and ERE with POSIX leftmost-longest
//     matching on the bounded VM, including back references and GNU word
//     assertions, exploring every path per start.
//   - Perl: the bounded pcre-common-v1 grammar on the bounded backtracking
//     engine with leftmost-first matching. It is never RE2.
//   - RE2: Go RE2 syntax and leftmost-first semantics (rg's default).
//
// Character policy: patterns and records are UTF-8 scalars (an invalid byte
// is one U+FFFD scalar); \w, \s, \b and GNU \< \> use fixed ASCII classes;
// -i is Unicode simple case folding. Every record search is metered against
// the catalog's regex_work_steps and allocation limits, with caller cancellation.
package textsearch

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// Dialect selects a pattern grammar and its matching semantics.
type Dialect string

const (
	Fixed    Dialect = "fixed"
	Basic    Dialect = "basic"
	Extended Dialect = "extended"
	Perl     Dialect = "perl"
	RE2      Dialect = "re2"
)

// Public diagnostic codes. Pattern errors are usage errors; a budget stop is
// a host-terminal refusal that discards the record's output.
const (
	CodeRegexInvalid     = "GREP_REGEX_INVALID"
	CodeRegexUnsupported = "GREP_REGEX_UNSUPPORTED"
	CodeMatchBudget      = "GREP_MATCH_BUDGET"
)

// Spec is one search pattern set. Each pattern may hold LF-separated
// alternatives, as grep -e does.
type Spec struct {
	Patterns   []string
	Dialect    Dialect
	IgnoreCase bool
	// Word requires each match to be neither preceded nor followed by an
	// ASCII word scalar (grep -w). Line requires the whole record (-x).
	Word bool
	Line bool
}

// Limits bound compilation and each record search.
type Limits struct {
	PatternBytes int64
	WorkSteps    int64
	MemoryBytes  int64
	InputBytes   int64
}

// CatalogLimits reads the frozen language catalog's regex ceilings.
func CatalogLimits() (Limits, error) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return Limits{}, err
	}
	values := map[string]int64{}
	for _, limit := range catalog.Language.Limits {
		values[limit.ID] = limit.Maximum
	}
	l := Limits{
		PatternBytes: values["regex_pattern_bytes"], WorkSteps: values["regex_work_steps"],
		MemoryBytes: values["evaluation_memory_bytes"],
		InputBytes:  values["regex_input_bytes"],
	}
	if l.PatternBytes <= 0 || l.WorkSteps <= 0 || l.MemoryBytes <= 0 || l.InputBytes <= 0 {
		return Limits{}, errors.New("regex limits missing from the language catalog")
	}
	return l, nil
}

// PatternError refuses a pattern before any record is searched.
type PatternError struct {
	Code    string
	Pattern string
	Offset  int
	Detail  string
}

func (e *PatternError) Error() string {
	return fmt.Sprintf("%s: pattern %q at byte %d: %s", e.Code, e.Pattern, e.Offset, e.Detail)
}

// BudgetError reports a record search that exceeded a regex ceiling.
// Resource is the catalog limit identifier.
type BudgetError struct {
	Resource          string
	Maximum, Observed int64
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("%s: %s maximum %d observed %d", CodeMatchBudget, e.Resource, e.Maximum, e.Observed)
}

// Span is a half-open byte range of a record.
type Span struct{ Start, End int }

// Selector decides whether one record (without its LF) is selected and
// reports the first match span the engine found.
type Selector interface {
	Select(ctx context.Context, record []byte) (Span, bool, error)
}

// Matcher is a compiled Spec.
type Matcher struct {
	spec    Spec
	limits  Limits
	engine  engine
	none    bool
	require litprefilter.Requirement
}

var _ Selector = (*Matcher)(nil)

// Compile validates and compiles every pattern. A pattern error names the
// first refused pattern; nothing partial is returned.
func Compile(ctx context.Context, spec Spec, limits Limits) (*Matcher, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	patterns := splitPatterns(spec.Patterns)
	m := &Matcher{spec: spec, limits: limits}
	if len(patterns) == 0 {
		m.none = true
		return m, nil
	}
	var err error
	switch spec.Dialect {
	case Fixed:
		m.engine, err = compileFixed(ctx, patterns, spec, limits)
	case Basic, Extended:
		m.engine, err = compilePOSIX(ctx, patterns, spec, limits)
	case Perl:
		m.engine, err = compilePerl(ctx, patterns, spec, limits)
	case RE2:
		m.engine, err = compileRE2(ctx, patterns, spec, limits)
	default:
		return nil, fmt.Errorf("unknown search dialect %q", spec.Dialect)
	}
	if err != nil {
		return nil, err
	}
	m.require = requirement(m.engine, patterns, spec)
	return m, nil
}

// Requirement is what a record must contain to hold any match, for index
// pruning; empty when the compiled engine cannot prove a required literal.
func (m *Matcher) Requirement() litprefilter.Requirement { return m.require }

func requirement(e engine, patterns []string, spec Spec) litprefilter.Requirement {
	switch e := e.(type) {
	case literalEngine:
		for _, p := range patterns {
			if p == "" {
				return litprefilter.Requirement{}
			}
		}
		return litprefilter.AnyOf(patterns...)
	case vmEngine:
		return e.required
	}
	return litprefilter.Requirement{}
}

// Select reports whether the record holds a match under the spec.
func (m *Matcher) Select(ctx context.Context, record []byte) (Span, bool, error) {
	if m.none {
		return Span{}, false, ctx.Err()
	}
	if err := m.admitRecord(record); err != nil {
		return Span{}, false, err
	}
	return m.engine.first(ctx, record)
}

// Each yields every nonempty match left to right. After a zero-width match
// the search advances one scalar, so it always terminates.
func (m *Matcher) Each(ctx context.Context, record []byte, yield func(Span) error) error {
	if m.none {
		return ctx.Err()
	}
	if err := m.admitRecord(record); err != nil {
		return err
	}
	return m.engine.each(ctx, record, yield)
}

// Groups yields every nonempty match with its capture groups (byte offsets,
// -1 when unset) for replacement. POSIX dialects report only group 0.
func (m *Matcher) Groups(ctx context.Context, record []byte, yield func([]int) error) error {
	if m.none {
		return ctx.Err()
	}
	if err := m.admitRecord(record); err != nil {
		return err
	}
	return m.engine.groups(ctx, record, yield)
}

// CaptureGroups is the number of capture groups a replacement may name.
func (m *Matcher) CaptureGroups() int {
	if m.none {
		return 0
	}
	return m.engine.captureGroups()
}

func (m *Matcher) admitRecord(record []byte) error {
	if int64(len(record)) > m.limits.InputBytes {
		return &BudgetError{Resource: "regex_input_bytes", Maximum: m.limits.InputBytes, Observed: int64(len(record))}
	}
	return nil
}

// splitPatterns applies grep's rule that LF separates patterns.
func splitPatterns(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		start := 0
		for i := 0; i < len(p); i++ {
			if p[i] == '\n' {
				out = append(out, p[start:i])
				start = i + 1
			}
		}
		out = append(out, p[start:])
	}
	return out
}

func checkPatternBytes(patterns []string, limits Limits) error {
	var total int64
	for _, p := range patterns {
		total += int64(len(p))
	}
	if total > limits.PatternBytes {
		return &BudgetError{Resource: "regex_pattern_bytes", Maximum: limits.PatternBytes, Observed: total}
	}
	return nil
}

// RegexpSelector adapts an already compiled Go regexp (the structured grep
// tool's RE2 contract) to the shared line scan.
type RegexpSelector struct{ Re *regexp.Regexp }

func (s RegexpSelector) Select(ctx context.Context, record []byte) (Span, bool, error) {
	if err := ctx.Err(); err != nil {
		return Span{}, false, err
	}
	loc := s.Re.FindIndex(record)
	if loc == nil {
		return Span{}, false, nil
	}
	return Span{loc[0], loc[1]}, true, nil
}
