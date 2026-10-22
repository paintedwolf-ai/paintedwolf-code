package commandsurface

import "github.com/lycaon/lycaon/internal/toolcontract"

// NodeID identifies one static syntax occurrence, independently of how many
// times execution reaches it. The containing source digest supplies identity.
type NodeID string

// SyntaxLocation identifies original UTF-8 bytes, before quote removal.
type SyntaxLocation struct {
	ID   NodeID
	Span toolcontract.SourceSpan
}

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
type CommandNode interface{ commandNode() }

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
	Name  string
	Value DeferredWord
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

// WhileLoop defers both its test and body until evaluation.
type WhileLoop struct {
	SyntaxLocation
	Condition []Statement
	Body      []Statement
}

func (WhileLoop) commandNode() {}

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

type ParameterPart struct {
	SyntaxLocation
	Name   string
	Length bool
}

func (ParameterPart) wordNode() {}

type SubstitutionPart struct {
	SyntaxLocation
	Backquotes bool
	Statements []Statement
}

func (SubstitutionPart) wordNode() {}
