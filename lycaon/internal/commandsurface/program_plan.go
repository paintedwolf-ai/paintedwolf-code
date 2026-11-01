package commandsurface

import (
	"math"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// NodeID identifies one static syntax occurrence, independently of how many
// times execution reaches it. The containing source digest supplies identity.
type NodeID string

// SyntaxLocation identifies original UTF-8 bytes, before quote removal.
type SyntaxLocation struct {
	ID   NodeID
	Span toolcontract.SourceSpan
}

func (l SyntaxLocation) syntaxLocation() SyntaxLocation { return l }

// ProgramSnapshot is a detached typed syntax tree. It carries no capabilities
// and contains no executable upstream interpreter objects.
type ProgramSnapshot struct {
	Version       int
	CatalogDigest string
	SourceDigest  string
	SourceName    string
	Statements    []Statement
}

// Statement retains dispatch modifiers and redirects in lexical order.
type Statement struct {
	SyntaxLocation
	Command    CommandNode
	Redirects  []ProgramRedirect
	Negated    bool
	Background bool
}

// CommandNode is the closed command grammar accepted by version 1.
type CommandNode interface {
	commandNode()
	syntaxLocation() SyntaxLocation
}

// SimpleCommand preserves assignments separately from deferred argument words.
type SimpleCommand struct {
	SyntaxLocation
	Assignments []Assignment
	Words       []DeferredWord
}

func (SimpleCommand) commandNode() {}

// Assignment changes a variable or scopes its value to a single dispatch.
type Assignment struct {
	SyntaxLocation
	Name   string
	Index  ArithmeticNode
	Append bool
	Value  DeferredWord
	Array  []DeferredWord
}

// EnvironmentDeclaration retains export assignments without joining or reparsing words.
type EnvironmentDeclaration struct {
	SyntaxLocation
	Assignments []Assignment
	Words       []DeferredWord
}

func (EnvironmentDeclaration) commandNode() {}

// StatementGroup distinguishes shared braces from an isolated parenthesis frame.
type StatementGroup struct {
	SyntaxLocation
	Isolated   bool
	Statements []Statement
}

func (StatementGroup) commandNode() {}

// BinaryCommand joins two statements without executing or expanding either.
type BinaryCommand struct {
	SyntaxLocation
	Operator string
	Left     Statement
	Right    Statement
}

func (BinaryCommand) commandNode() {}

// Conditional represents if, elif and else without eager branch expansion.
type Conditional struct {
	SyntaxLocation
	Condition []Statement
	Then      []Statement
	Else      *Conditional
}

func (Conditional) commandNode() {}

// WhileLoop defers both its test and body until evaluation. Until runs the
// body while the condition fails instead of while it succeeds.
type WhileLoop struct {
	SyntaxLocation
	Condition []Statement
	Body      []Statement
	Until     bool
}

func (WhileLoop) commandNode() {}

// CaseStatement runs the body of the first item with a glob pattern matching
// the word. Every item ends with ;; (no fallthrough).
type CaseStatement struct {
	SyntaxLocation
	Word  DeferredWord
	Items []CaseItem
}

type CaseItem struct {
	SyntaxLocation
	Patterns []DeferredWord
	Body     []Statement
}

func (CaseStatement) commandNode() {}

// ForLoop distinguishes omitted in from an explicitly empty iteration list.
type ForLoop struct {
	SyntaxLocation
	Variable   string
	Positional bool
	Items      []DeferredWord
	Body       []Statement
}

func (ForLoop) commandNode() {}

// FunctionDeclaration records the supported NAME() { ...; } spelling.
type FunctionDeclaration struct {
	SyntaxLocation
	Name string
	Body Statement
}

func (FunctionDeclaration) commandNode() {}

// TestExpression preserves the typed [[ ... ]] expression tree.
type TestExpression struct {
	SyntaxLocation
	Expression TestNode
}

func (TestExpression) commandNode() {}

// ArithmeticCommand evaluates (( expression )) for numeric status and mutations.
type ArithmeticCommand struct {
	SyntaxLocation
	Expression ArithmeticNode
}

func (ArithmeticCommand) commandNode() {}

// TrapCommand registers or clears the invocation-exit handler.
type TrapCommand struct {
	SyntaxLocation
	Statements []Statement
}

func (TrapCommand) commandNode() {}

// TimedCommand is the time reserved word before a pipeline; Pipeline is
// empty for a bare time. POSIX selects the -p report.
type TimedCommand struct {
	SyntaxLocation
	POSIX    bool
	Pipeline []Statement
}

func (TimedCommand) commandNode() {}

// TestNode is a word, unary predicate, binary predicate or parenthesized test.
type TestNode interface{ testNode() }

type TestWord struct{ Word DeferredWord }

func (TestWord) testNode() {}

type UnaryPredicate struct {
	SyntaxLocation
	Operator string
	Operand  TestNode
}

func (UnaryPredicate) testNode() {}

type BinaryPredicate struct {
	SyntaxLocation
	Operator    string
	Left, Right TestNode
}

func (BinaryPredicate) testNode() {}

type GroupedPredicate struct {
	SyntaxLocation
	Expression TestNode
}

func (GroupedPredicate) testNode() {}

// ProgramRedirect keeps descriptor binding ordered, including here-documents.
type ProgramRedirect struct {
	SyntaxLocation
	Operator     string
	Descriptor   int
	Target       DeferredWord
	HereDocument *DeferredWord
}

// DeferredWord retains quotation and expansion nodes; data is never reparsed.
type DeferredWord struct {
	SyntaxLocation
	Parts []WordNode
}

type WordNode interface{ wordNode() }

type LiteralPart struct {
	SyntaxLocation
	Value string
}

func (LiteralPart) wordNode() {}

type SingleQuotedPart struct {
	SyntaxLocation
	Value string
}

func (SingleQuotedPart) wordNode() {}

type DoubleQuotedPart struct {
	SyntaxLocation
	Parts []WordNode
}

func (DoubleQuotedPart) wordNode() {}

// BracePart is one brace expansion in a command word, for-loop item or array
// element. Before any other expansion, the word becomes one word per
// alternative, or per sequence value, in order; nested braces multiply.
type BracePart struct {
	SyntaxLocation
	Alternatives []DeferredWord
	Sequence     *BraceSequence
}

func (BracePart) wordNode() {}

// BraceSequence is {start..end[..step]}: decimal integers zero-padded to Width,
// or single-byte letters. Step is the absolute step; the direction runs from
// Start toward End.
type BraceSequence struct {
	Start, End int64
	Step       uint64
	Width      int
	Letters    bool
}

// Count is the number of values, at least one.
func (s BraceSequence) Count() uint64 {
	span := unsignedSpan(s.End, s.Start)
	if s.End < s.Start {
		span = unsignedSpan(s.Start, s.End)
	}
	steps := span / max(s.Step, 1)
	if steps == ^uint64(0) {
		return steps
	}
	return steps + 1
}

func unsignedSpan(high, low int64) uint64 {
	if low >= 0 || high < 0 {
		diff := high - low
		if diff >= 0 {
			return uint64(diff)
		}
	}
	var highMag, lowMag uint64
	if high >= 0 {
		highMag = uint64(high)
	}
	if low == math.MinInt64 {
		lowMag = uint64(math.MaxInt64) + 1
	} else if negLow := -low; negLow >= 0 {
		lowMag = uint64(negLow)
	}
	return highMag + lowMag
}

// Value is the i-th value, for i below Count.
func (s BraceSequence) Value(i uint64) int64 {
	offset := i * max(s.Step, 1)
	const maxStep = uint64(math.MaxInt64)
	first := clampUint64ToInt64(offset)
	rest := offset - min(offset, maxStep)
	second := clampUint64ToInt64(rest)
	third := clampUint64ToInt64(rest - min(rest, maxStep))
	if s.End < s.Start {
		return s.Start - first - second - third
	}
	return s.Start + first + second + third
}

func clampUint64ToInt64(v uint64) int64 {
	if v <= math.MaxInt64 {
		return int64(v)
	}
	return math.MaxInt64
}

// ParameterSlice retains ${name:offset} and ${name:offset:length} bounds.
type ParameterSlice struct {
	Offset ArithmeticNode
	Length ArithmeticNode
}

// ParameterPart is $name, ${name}, ${#name}, or ${name OP word}. Operator is
// one of -, :-, +, :+, ?, :?, #, ##, %, %%, ^, ^^, ",", ",,", /, //, /# or
// /%; Argument is its word or pattern and Replacement the replacement of a
// substitution operator.
type ParameterPart struct {
	SyntaxLocation
	Name        string
	Subscript   string
	Index       ArithmeticNode
	Length      bool
	Operator    string
	Argument    *DeferredWord
	Replacement *DeferredWord
	Slice       *ParameterSlice
}

func (ParameterPart) wordNode() {}

// Words lists the operator words nested in the parameter.
func (p ParameterPart) Words() []DeferredWord {
	var out []DeferredWord
	if p.Index != nil {
		out = append(out, arithmeticWords(p.Index)...)
	}
	if p.Slice != nil {
		out = append(out, arithmeticWords(p.Slice.Offset)...)
		out = append(out, arithmeticWords(p.Slice.Length)...)
	}
	for _, word := range []*DeferredWord{p.Argument, p.Replacement} {
		if word != nil {
			out = append(out, *word)
		}
	}
	return out
}

type SubstitutionPart struct {
	SyntaxLocation
	Operator   string
	Backquotes bool
	Statements []Statement
}

func (SubstitutionPart) wordNode() {}
