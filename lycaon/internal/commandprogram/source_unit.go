package commandprogram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// SourceIntegrityError identifies a capture that cannot supply one complete,
// authoritative syntax unit. It is not evidence of zero earlier parent effects.
type SourceIntegrityError struct{ SourceID string }

func (e *SourceIntegrityError) Error() string {
	return "captured source identity or byte digest is inconsistent"
}

// LoadSourceUnit captures through the source owner when the source statement is
// reached, verifies exact bytes, and validates the entire captured syntax unit.
// It performs no dispatch, aggregate settlement or replay of the parent program.
func LoadSourceUnit(ctx context.Context, capture SourceCapture, invocation toolcontract.OperationInvocation, path string) (*commandsurface.ParsedProgram, CapturedSource, error) {
	if err := invocation.Validate(); err != nil {
		return nil, CapturedSource{}, err
	}
	if capture == nil {
		return nil, CapturedSource{}, fmt.Errorf("source capture binding unavailable")
	}
	source, err := capture.Capture(ctx, invocation, path)
	if err != nil {
		return nil, CapturedSource{}, err
	}
	maximum := commandsurface.SyntaxUnitByteLimit(true)
	if int64(len(source.Bytes)) > maximum {
		return nil, CapturedSource{Pin: source.Pin}, &commandsurface.SyntaxDiagnostic{Code: "COMMAND_LIMIT_EXCEEDED", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, Expected: "bounded captured source bytes", Limit: "source_bytes", Maximum: maximum, Observed: int64(len(source.Bytes))}
	}
	source.Bytes = bytes.Clone(source.Bytes)
	pin := source.Pin
	if pin.Representation != toolcontract.UTF8SourceBytes {
		return nil, source, &commandsurface.SyntaxDiagnostic{Code: "COMMAND_OPERAND_INVALID", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, Expected: "source scripts captured as UTF-8 without BOM"}
	}
	sum := sha256.Sum256(source.Bytes)
	if pin.SourceID == "" || pin.Revision == "" || pin.StartByte != 0 || pin.EndByte != int64(len(source.Bytes)) || pin.SHA256 != hex.EncodeToString(sum[:]) {
		return nil, CapturedSource{}, &SourceIntegrityError{SourceID: pin.SourceID}
	}
	parsed, err := commandsurface.ParseProgram(source.Bytes, commandsurface.ParseOptions{SourceName: pin.SourceID, Sourced: true, Version: toolcontract.LanguageVersion, CatalogDigest: invocation.CatalogDigest})
	if err != nil {
		return nil, source, err
	}
	return parsed, source, nil
}
