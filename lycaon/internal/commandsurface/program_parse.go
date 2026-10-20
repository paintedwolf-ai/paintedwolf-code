package commandsurface

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/toolcontract"
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
	Maximum  int64
	Observed int64
}

func (d *SyntaxDiagnostic) Error() string {
	return fmt.Sprintf("%s at %d:%d: %s", d.Code, d.Span.Line, d.Span.Column, d.Expected)
}

// ParsedProgram retains a private validated tree; Snapshot returns detached data.
// Neither parsing nor a snapshot grants permission to dispatch an operation.
type ParsedProgram struct{ plan ProgramSnapshot }

func (p *ParsedProgram) Snapshot() ProgramSnapshot { return cloneProgram(p.plan) }

// ParseProgram validates the complete syntax unit before exposing any plan.
// It only uses the upstream syntax package, never its interpreter or executor.
func ParseProgram(source []byte, options ParseOptions) (*ParsedProgram, error) {
	catalog, catalogErr := toolcontract.LanguageV1()
	if catalogErr != nil {
		return nil, fmt.Errorf("load compiled command catalog: %w", catalogErr)
	}
	maximum := languageLimit(catalog, "program_bytes")
	if options.Sourced {
		maximum = languageLimit(catalog, "source_bytes")
	}
	if int64(len(source)) > maximum {
		return nil, &SyntaxDiagnostic{Code: "COMMAND_LIMIT_EXCEEDED", Span: unitStart(), Expected: "a bounded syntax unit", Limit: map[bool]string{true: "source_bytes", false: "program_bytes"}[options.Sourced], Maximum: maximum, Observed: int64(len(source))}
	}
	if options.Version != 0 && options.Version != toolcontract.LanguageVersion {
		return nil, &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_UNSUPPORTED", Span: unitStart(), Expected: "language version 1"}
	}
	if !utf8.Valid(source) || bytes.IndexByte(source, 0) >= 0 || bytes.HasPrefix(source, []byte{0xef, 0xbb, 0xbf}) {
		return nil, &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_INVALID", Span: unitStart(), Expected: "UTF-8 source without BOM or NUL"}
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(bytes.NewReader(source), options.SourceName)
	if err != nil {
		return nil, parserDiagnostic(err, len(source))
	}
	c := programCompiler{catalog: catalog, source: source}
	c.validate(file)
	if c.err != nil {
		return nil, c.err
	}
	statements := c.statements(file.Stmts)
	if c.err != nil {
		return nil, c.err
	}
	sourceHash := sha256.Sum256(source)
	digest := options.CatalogDigest
	if digest == "" {
		raw, _ := json.Marshal(catalog)
		sum := sha256.Sum256(raw)
		digest = hex.EncodeToString(sum[:])
	}
	return &ParsedProgram{plan: ProgramSnapshot{Version: toolcontract.LanguageVersion, CatalogDigest: digest, SourceDigest: hex.EncodeToString(sourceHash[:]), SourceName: options.SourceName, Statements: statements}}, nil
}

func languageLimit(c toolcontract.LanguageCatalog, id string) int64 {
	for _, limit := range c.Language.Limits {
		if limit.ID == id {
			return limit.Maximum
		}
	}
	panic("missing compiled command language limit: " + id)
}
func unitStart() toolcontract.SourceSpan { return toolcontract.SourceSpan{Line: 1, Column: 1} }
func parserDiagnostic(err error, length int) error {
	span := unitStart()
	var parse syntax.ParseError
	if errors.As(err, &parse) {
		start := min(int(parse.Pos.Offset()), length)
		span = toolcontract.SourceSpan{StartByte: start, EndByte: min(start+1, length), Line: int(parse.Pos.Line()), Column: int(parse.Pos.Col())}
	}
	return &SyntaxDiagnostic{Code: "COMMAND_SYNTAX_INVALID", Span: span, Expected: "a complete version 1 syntax form"}
}

type programCompiler struct {
	catalog        toolcontract.LanguageCatalog
	source         []byte
	sequence       int
	literalContext map[*syntax.Word]bool
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
		c.err = &SyntaxDiagnostic{Code: "COMMAND_LIMIT_EXCEEDED", Span: nodeSpan(n), Expected: "bounded " + id, Limit: id, Maximum: maximum, Observed: observed}
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
	var out strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			out.WriteString(p.Value)
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			out.WriteString(p.Value)
		case *syntax.DblQuoted:
			text, ok := c.literal(&syntax.Word{Parts: p.Parts})
			if !ok || p.Dollar {
				return "", false
			}
			out.WriteString(text)
		default:
			return "", false
		}
	}
	return out.String(), true
}
