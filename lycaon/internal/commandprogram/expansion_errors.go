package commandprogram

import (
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

const (
	codeLimitExceeded     = "COMMAND_LIMIT_EXCEEDED"
	codeOperandInvalid    = "COMMAND_OPERAND_INVALID"
	codeSyntaxUnsupported = "COMMAND_SYNTAX_UNSUPPORTED"
)

// ExpansionError is a typed refusal of expansion or invocation-variable state,
// located at the word part that caused it. Code is the catalog diagnostic;
// Limit, Unit, Maximum and Observed describe an exceeded catalog ceiling.
// The containing command never dispatches after one.
type ExpansionError struct {
	Code             string
	Span             toolcontract.SourceSpan
	Expected         string
	Limit            string
	Unit             string
	Maximum          int64
	Observed         int64
	UnsignedObserved uint64
}

func (e *ExpansionError) Error() string {
	if e.Limit != "" {
		return fmt.Sprintf("%s at %d:%d: %s %d > %d %s", e.Code, e.Span.Line, e.Span.Column, e.Limit, e.ObservedValue(), e.Maximum, e.Unit)
	}
	return fmt.Sprintf("%s at %d:%d: %s", e.Code, e.Span.Line, e.Span.Column, e.Expected)
}

// ObservedValue preserves an exact positive index beyond signed arithmetic's
// range while existing resource counters retain their signed representation.
func (e *ExpansionError) ObservedValue() any {
	if e.UnsignedObserved != 0 {
		return e.UnsignedObserved
	}
	return e.Observed
}

// SubstitutionError is a reached substitution that failed: its port refused or
// its unit ended in a host stop. Result retains the unit's producer facts, so
// the evaluator can latch them; earlier recorded effects remain.
type SubstitutionError struct {
	Node   commandsurface.NodeID
	Span   toolcontract.SourceSpan
	Result toolcontract.ProducerResult
	Cause  error
}

func (e *SubstitutionError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("substitution at %d:%d failed: %v", e.Span.Line, e.Span.Column, e.Cause)
	}
	return fmt.Sprintf("substitution at %d:%d ended %s %s", e.Span.Line, e.Span.Column, e.Result.Terminal, e.Result.StopCode)
}

func (e *SubstitutionError) Unwrap() error { return e.Cause }

// catalogBounds are the frozen catalog ceilings and their units by limit id.
type catalogBounds struct {
	maximum        map[string]int64
	unit           map[string]string
	arrayIndexRule string
}

var languageBounds = sync.OnceValues(func() (catalogBounds, error) {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return catalogBounds{}, err
	}
	bounds := catalogBounds{maximum: map[string]int64{}, unit: map[string]string{}, arrayIndexRule: catalog.Language.Rules["array_index"]}
	for _, limit := range catalog.Language.Limits {
		bounds.maximum[limit.ID], bounds.unit[limit.ID] = limit.Maximum, limit.Unit
	}
	for _, id := range []string{"argument_bytes", "argv_bytes", "expanded_fields", "glob_matches", "traversal_visits", "substitution_bytes", "substitution_depth", "variable_value_bytes", "here_document_bytes", "expansion_memory_bytes", "variable_count", "environment_bytes"} {
		if bounds.maximum[id] < 1 {
			return catalogBounds{}, fmt.Errorf("compiled language is missing limit %s", id)
		}
	}
	return bounds, nil
})

func (b catalogBounds) exceeded(id string, observed int64, span toolcontract.SourceSpan) *ExpansionError {
	return &ExpansionError{Code: codeLimitExceeded, Span: span, Expected: "bounded " + id, Limit: id, Unit: b.unit[id], Maximum: b.maximum[id], Observed: observed}
}

// expansionLimits are the ceilings word expansion reads on every word.
type expansionLimits struct {
	catalogBounds
	argumentBytes, argvBytes, fields, globMatches, visits int64
	substitutionBytes, substitutionDepth, valueBytes      int64
	documentBytes, memory                                 int64
}

func catalogExpansionLimits() (expansionLimits, error) {
	bounds, err := languageBounds()
	if err != nil {
		return expansionLimits{}, err
	}
	m := bounds.maximum
	return expansionLimits{catalogBounds: bounds,
		argumentBytes: m["argument_bytes"], argvBytes: m["argv_bytes"], fields: m["expanded_fields"],
		globMatches: m["glob_matches"], visits: m["traversal_visits"], substitutionBytes: m["substitution_bytes"],
		substitutionDepth: m["substitution_depth"], valueBytes: m["variable_value_bytes"],
		documentBytes: m["here_document_bytes"], memory: m["expansion_memory_bytes"]}, nil
}

func invalidValue(span toolcontract.SourceSpan, expected string) *ExpansionError {
	return &ExpansionError{Code: codeOperandInvalid, Span: span, Expected: expected}
}
