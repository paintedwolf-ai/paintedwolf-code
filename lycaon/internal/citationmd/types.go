// Package citationmd parses the inline citation grammar of agent Markdown:
// typed occurrences with source spans, literal zones, and explicit issues. It
// knows syntax only. Evidence resolution belongs to its callers.
package citationmd

import "github.com/lycaon/lycaon/internal/verification"

const (
	// MaxInlineProtocolNodeBytes bounds one custom node, delimiters included.
	MaxInlineProtocolNodeBytes = 4096
	// MaxSourceBytes bounds one parsed document.
	MaxSourceBytes = 2 << 20
	// MaxOccurrences bounds the occurrences one document may record.
	MaxOccurrences = 1024
	// maxIssueTokenBytes bounds the source text an issue carries.
	maxIssueTokenBytes = 256
)

// Options configures one parse or projection.
type Options struct {
	// KnownPath reports whether a single-segment name matches a path observed
	// in the current window. Nil means no name matches.
	KnownPath func(name string) bool
	// MaxSourceBytes and MaxOccurrences override the declared budgets.
	MaxSourceBytes int
	MaxOccurrences int
}

func (o Options) known(name string) bool { return o.KnownPath != nil && o.KnownPath(name) }

func (o Options) sourceBudget() int {
	if o.MaxSourceBytes > 0 {
		return o.MaxSourceBytes
	}
	return MaxSourceBytes
}

func (o Options) occurrenceBudget() int {
	if o.MaxOccurrences > 0 {
		return o.MaxOccurrences
	}
	return MaxOccurrences
}

// Span is a half-open byte range of the source.
type Span struct {
	Start int
	End   int
}

// NodeKind classifies one occurrence.
type NodeKind string

const (
	KindCitation      NodeKind = "citation"
	KindWebLink       NodeKind = "web_link"
	KindWorkspaceLink NodeKind = "workspace_link"
	KindArtifact      NodeKind = "artifact"
	KindVerify        NodeKind = "verify"
	KindRepair        NodeKind = "repair"
)

// Citation is a typed evidence reference. EndLine is zero for a single line or
// no line; a one-line range normalizes to zero.
type Citation struct {
	Handle  string
	Path    string
	Line    int
	EndLine int
	Excerpt string
}

// LinkForm is the Markdown syntax that produced a link.
type LinkForm string

const (
	LinkInline    LinkForm = "inline"
	LinkReference LinkForm = "reference"
	LinkAutolink  LinkForm = "autolink"
)

// Link is a standard Markdown link. Destination is the resolved destination,
// for a reference link the one its definition names.
type Link struct {
	Form        LinkForm
	Label       string
	LabelSpan   Span
	Destination string
}

// Repair is a parsed `[cite: c1 => value]` directive. Whether it applies is the
// caller's decision. Exactly one of Citation and URL is set.
type Repair struct {
	Target   string
	Citation *Citation
	URL      string
}

// Occurrence is one protocol reference in document order. Members of a group
// or artifact list are separate occurrences sharing Node.
type Occurrence struct {
	Index    int
	Kind     NodeKind
	Span     Span
	Node     Span
	Token    string
	Citation *Citation
	Link     *Link
	Artifact string
	Verify   *verification.Assessment
	Repair   *Repair
}

// IssueCode names a structured grammar defect.
type IssueCode string

const (
	IssueMalformedCitation       IssueCode = "malformed_citation"
	IssueInvalidRange            IssueCode = "invalid_range"
	IssueMalformedArtifact       IssueCode = "malformed_artifact"
	IssueMalformedRepair         IssueCode = "malformed_repair"
	IssueInvalidVerification     IssueCode = "invalid_verification"
	IssueConflictingVerification IssueCode = "conflicting_verification"
	IssueUnterminatedNode        IssueCode = "unterminated_node"
	IssueNodeTooLong             IssueCode = "node_too_long"
	IssueOccurrenceBudget        IssueCode = "occurrence_budget_exceeded"
	IssueSourceBudget            IssueCode = "source_budget_exceeded"
)

// Issue is one explicit parse defect. Token holds a bounded prefix of the span.
type Issue struct {
	Code  IssueCode
	Kind  NodeKind
	Span  Span
	Token string
}

// ZoneKind classifies a literal zone.
type ZoneKind string

const (
	ZoneFence      ZoneKind = "fence"
	ZoneIndented   ZoneKind = "indented_code"
	ZoneHTMLBlock  ZoneKind = "html_block"
	ZoneDefinition ZoneKind = "link_definition"
	ZoneCodeSpan   ZoneKind = "code_span"
	ZoneRawHTML    ZoneKind = "raw_html"
	ZoneLink       ZoneKind = "link"
	ZoneImage      ZoneKind = "image"
	ZoneAutolink   ZoneKind = "autolink"
)

// Zone is a source range the custom scanner never enters. Links, images and
// autolinks are zones because standard Markdown takes precedence over custom
// brackets; a link citation is reported as an occurrence, not from its label.
type Zone struct {
	Kind ZoneKind
	Span Span
}

// Document is one parsed source. Spans index Source.
type Document struct {
	Source      string
	Occurrences []Occurrence
	Issues      []Issue
	// Zones are the literal zones the parse honoured, in source order.
	Zones []Zone
	parts []part
	// overBudget means the source was refused unparsed and has no safe display.
	overBudget bool
}

// partKind is how a non-text region of the source presents.
type partKind int

const (
	partLiteral partKind = iota
	partLink
	partNode
	partDamaged
)

// part is one ordered non-text region. first and count index Occurrences;
// count is zero for a node past the occurrence budget.
type part struct {
	kind  partKind
	span  Span
	first int
	count int
}

// boundedToken returns at most maxIssueTokenBytes of token without splitting a
// rune.
func boundedToken(token string) string {
	if len(token) <= maxIssueTokenBytes {
		return token
	}
	cut := maxIssueTokenBytes
	for cut > 0 && token[cut]&0xC0 == 0x80 {
		cut--
	}
	return token[:cut]
}
