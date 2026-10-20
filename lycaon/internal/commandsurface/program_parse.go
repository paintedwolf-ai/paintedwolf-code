package commandsurface

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// ParseOptions identifies one submitted or captured source unit. Sourced units
// have their own byte ceiling; loading one never reparses its parent program.
type ParseOptions struct {
	SourceName    string
	Sourced       bool
	Version       int
	CatalogDigest string
}

// SyntaxDiagnostic is a typed admission refusal. Diagnostic text is never an
// authority decision or a proof of an exact source correction.
type SyntaxDiagnostic struct {
	Code     string
	Span     toolcontract.SourceSpan
	Expected string
	Limit    string
	Unit     string
	Maximum  int64
	Observed int64
}

func (d *SyntaxDiagnostic) Error() string {
	return fmt.Sprintf("%s at %d:%d: %s", d.Code, d.Span.Line, d.Span.Column, d.Expected)
}

// ParsedProgram retains a private validated tree; Snapshot returns detached data.
// Neither parsing nor a snapshot grants permission to dispatch an operation.
type ParsedProgram struct {
	plan         ProgramSnapshot
	source       []byte
	stateBytes   int64
	releaseState func()
	mu           sync.RWMutex
	closed       bool
}

// Snapshot is a detached inspection copy. Execution uses SnapshotWithMemory so
// simultaneous copies remain charged to the enclosing aggregate state.
func (p *ParsedProgram) Snapshot() ProgramSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneProgram(p.plan)
}

// Source returns the original unit bytes for exact span inspection, not replay.
func (p *ParsedProgram) Source() []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return bytes.Clone(p.source)
}

var programLanguage = sync.OnceValues(toolcontract.LanguageV1)

// SyntaxUnitByteLimit reports the compiled pre-allocation input ceiling.
func SyntaxUnitByteLimit(sourced bool) int64 {
	catalog, err := programLanguage()
	if err != nil {
		return 0
	}
	id := "program_bytes"
	if sourced {
		id = "source_bytes"
	}
	return languageLimit(catalog, id)
}

// ParseProgram validates the complete syntax unit before exposing any plan.
// This pure entry applies local bounds; execution uses NewProgramParser to
// reserve scratch against its enclosing program before capture or parsing.
// It only uses the upstream syntax package, never its interpreter or executor.
func ParseProgram(source []byte, options ParseOptions) (*ParsedProgram, error) {
	return parseProgram(context.Background(), source, options, nil)
}

func parseProgram(ctx context.Context, source []byte, options ParseOptions, memory ProgramMemoryAdmission) (*ParsedProgram, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalog, catalogErr := programLanguage()
	if catalogErr != nil {
		return nil, fmt.Errorf("load compiled command catalog: %w", catalogErr)
	}
	maximum := languageLimit(catalog, "program_bytes")
	if options.Sourced {
		maximum = languageLimit(catalog, "source_bytes")
	}
	if int64(len(source)) > maximum {
		return nil, &SyntaxDiagnostic{Code: "COMMAND_LIMIT_EXCEEDED", Span: unitStart(), Expected: "a bounded syntax unit", Limit: map[bool]string{true: "source_bytes", false: "program_bytes"}[options.Sourced], Unit: "bytes", Maximum: maximum, Observed: int64(len(source))}
	}
	if options.Version != 0 && options.Version != toolcontract.LanguageVersion {
		return nil, &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_UNSUPPORTED", Span: unitStart(), Expected: "language version 1"}
	}
	if diagnostic := programEncodingDiagnostic(source, options.Sourced); diagnostic != nil {
		return nil, diagnostic
	}

	if options.CatalogDigest != "" && options.CatalogDigest != toolcontract.LanguageCatalogDigest() {
		return nil, &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_UNSUPPORTED", Span: unitStart(), Expected: "the frozen compiled language/catalog identity"}
	}
	pool, err := syntax.NewAllocationBudget(languageLimit(catalog, "evaluation_memory_bytes"))
	if err != nil {
		return nil, err
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.WithBudget(syntax.Budget{Pool: pool})).Parse(contextSource{ctx: ctx, reader: bytes.NewReader(source)}, options.SourceName)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, parserDiagnostic(err, len(source))
	}
	c := programCompiler{catalog: catalog, source: source, pool: pool}
	c.validate(file)
	if c.err != nil {
		return nil, c.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// This envelope bounds the selected host representations, including boxed
	// union values, old/new slice backing storage, node IDs and cloned strings.
	// The 2 KiB/node allowance exceeds the largest host union (BinaryCommand),
	// its slice-growth overlap and synthesized declaration words. Source text is
	// cloned at most eight times across capture/lowering metadata. Host recursion
	// shares the selected parser's conservative 4 KiB active-frame envelope.
	hostBytes := c.nodeCount*2048 + int64(len(source))*8 + (c.maximumDepth+1)*4096 + (64 << 10)
	var releaseState func()
	retained := false
	if memory != nil {
		releaseState, err = memory.ReserveProgramState(ctx, hostBytes)
		if err != nil {
			return nil, err
		}
		if releaseState == nil {
			return nil, fmt.Errorf("program state admission returned no release")
		}
		defer func() {
			if !retained {
				releaseState()
			}
		}()
	}
	if !c.reserveHeap(hostBytes, file) {
		return nil, c.err
	}
	statements := c.statements(file.Stmts)
	if c.err != nil {
		return nil, c.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sourceHash := sha256.Sum256(source)
	digest := toolcontract.LanguageCatalogDigest()
	retained = true
	return &ParsedProgram{source: bytes.Clone(source), stateBytes: hostBytes, releaseState: releaseState, plan: ProgramSnapshot{Version: toolcontract.LanguageVersion, CatalogDigest: digest, SourceDigest: hex.EncodeToString(sourceHash[:]), SourceName: options.SourceName, Statements: statements}}, nil
}

func languageLimit(c toolcontract.LanguageCatalog, id string) int64 {
	for _, limit := range c.Language.Limits {
		if limit.ID == id {
			return limit.Maximum
		}
	}
	panic("missing compiled command language limit: " + id)
}
func languageLimitUnit(c toolcontract.LanguageCatalog, id string) string {
	for _, limit := range c.Language.Limits {
		if limit.ID == id {
			return limit.Unit
		}
	}
	panic("missing compiled command language limit: " + id)
}
func unitStart() toolcontract.SourceSpan { return toolcontract.SourceSpan{Line: 1, Column: 1} }
func parserDiagnostic(err error, length int) error {
	var budget *syntax.BudgetError
	if errors.As(err, &budget) {
		return &SyntaxDiagnostic{Code: "COMMAND_LIMIT_EXCEEDED", Span: parserPosition(budget.Pos, length), Expected: "bounded parser working storage", Limit: "evaluation_memory_bytes", Unit: "bytes", Maximum: budget.Maximum, Observed: budget.Observed}
	}
	var dialect syntax.LangError
	if errors.As(err, &dialect) {
		return &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_UNSUPPORTED", Span: parserPosition(dialect.Pos, length), Expected: "a supported version 1 syntax form"}
	}
	span := unitStart()
	var parse syntax.ParseError
	if errors.As(err, &parse) {
		span = parserPosition(parse.Pos, length)
	}
	return &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_INVALID", Span: span, Expected: "a complete version 1 syntax form"}
}

func parserPosition(pos syntax.Pos, length int) toolcontract.SourceSpan {
	if !pos.IsValid() {
		return unitStart()
	}
	start := min(int(pos.Offset()), length)
	return toolcontract.SourceSpan{StartByte: start, EndByte: min(start+1, length), Line: int(pos.Line()), Column: int(pos.Col())}
}

type programCompiler struct {
	catalog        toolcontract.LanguageCatalog
	source         []byte
	sequence       int
	literalContext map[*syntax.Word]bool
	testWords      map[*syntax.Word]bool
	staticProof    map[*syntax.Word]bool
	nodeCount      int64
	maximumDepth   int64
	pool           *syntax.AllocationBudget
	err            error
}

func (c *programCompiler) location(n syntax.Node) SyntaxLocation {
	c.sequence++
	return SyntaxLocation{ID: NodeID(fmt.Sprintf("n%d", c.sequence)), Span: nodeSpan(n)}
}
func nodeSpan(n syntax.Node) toolcontract.SourceSpan {
	if w, ok := n.(*syntax.Word); ok && len(w.Parts) == 0 {
		return unitStart()
	}
	return toolcontract.SourceSpan{StartByte: int(n.Pos().Offset()), EndByte: int(n.End().Offset()), Line: int(n.Pos().Line()), Column: int(n.Pos().Col())}
}
func (c *programCompiler) unsupported(n syntax.Node, expected string) {
	if c.err == nil {
		c.err = &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_UNSUPPORTED", Span: nodeSpan(n), Expected: expected}
	}
}
func (c *programCompiler) limit(n syntax.Node, id string, observed int64) {
	maximum := languageLimit(c.catalog, id)
	if c.err == nil && observed > maximum {
		c.err = &SyntaxDiagnostic{Code: "COMMAND_LIMIT_EXCEEDED", Span: nodeSpan(n), Expected: "bounded " + id, Limit: id, Unit: languageLimitUnit(c.catalog, id), Maximum: maximum, Observed: observed}
	}
}
func (c *programCompiler) nativeName(name string) bool {
	for _, command := range c.catalog.Commands {
		if command.Name == name {
			return true
		}
		for _, alias := range command.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}
func (c *programCompiler) literal(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", true
	}
	value, known, err := expand.StaticLiteral(w, c.pool)
	if err != nil {
		c.err = parserDiagnostic(err, len(c.source))
		return "", false
	}
	if known && !c.staticProof[w] {
		// Reserve a conservative map-entry/growth envelope before inserting proof.
		if !c.reserveHeap(256, w) {
			return "", false
		}
		if c.staticProof == nil {
			c.staticProof = make(map[*syntax.Word]bool)
		}
		c.staticProof[w] = true
	}
	return value, known
}

func (c *programCompiler) reserveHeap(bytes int64, node syntax.Node) bool {
	if c.err != nil {
		return false
	}
	pos := syntax.Pos{}
	if node != nil {
		pos = node.Pos()
	}
	if err := c.pool.ReserveHeap(bytes, pos); err != nil {
		c.err = parserDiagnostic(err, len(c.source))
		return false
	}
	return true
}
