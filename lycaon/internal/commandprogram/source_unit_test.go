package commandprogram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type sourceCaptureFunc func(context.Context, toolcontract.OperationInvocation, string) (CapturedSource, error)

func (f sourceCaptureFunc) Capture(ctx context.Context, in toolcontract.OperationInvocation, path string) (CapturedSource, error) {
	return f(ctx, in, path)
}
func capturedFixture(raw []byte) CapturedSource {
	hash := sha256.Sum256(raw)
	return CapturedSource{Bytes: raw, Pin: toolcontract.SourcePin{SourceID: "fixture-source", Revision: "revision-2", Representation: toolcontract.UTF8SourceBytes, SHA256: hex.EncodeToString(hash[:]), EndByte: int64(len(raw))}}
}
func sourceInvocation() toolcontract.OperationInvocation {
	return toolcontract.OperationInvocation{Identity: toolcontract.ExecutionIdentity{ProgramID: "program-1", NodeID: "source-node", ExecutionID: "execution-2"}, ActorID: "fixture-actor", SessionID: "fixture-session", Scope: toolcontract.OperationScope{ProjectID: "fixture-project"}, SourceUnitID: "parent-unit", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: "fixture-catalog"}
}
func TestCapturedSourceUnitUsesExactOwnerBytesAndPreservesIdentity(t *testing.T) {
	raw := []byte("printf '%s' \"$1\"\n")
	capture := sourceCaptureFunc(func(ctx context.Context, in toolcontract.OperationInvocation, path string) (CapturedSource, error) {
		if path != "./script.pw" || in.Identity.ExecutionID != "execution-2" {
			t.Fatalf("capture changed invocation: %q %+v", path, in)
		}
		return capturedFixture(raw), nil
	})
	p, captured, err := LoadSourceUnit(t.Context(), capture, sourceInvocation(), "./script.pw")
	testutil.FailErr(t, "capture and validate source", err)
	raw[0] = 'x'
	if string(p.Source()) != "printf '%s' \"$1\"\n" || captured.Pin.Revision != "revision-2" || p.Snapshot().SourceDigest != captured.Pin.SHA256 {
		t.Fatalf("captured different source: %+v", captured.Pin)
	}
	captured.Bytes[0] = 'y'
	if p.Source()[0] != 'p' {
		t.Fatal("retained parser bytes were mutable through capture")
	}
	if p.Snapshot().CatalogDigest != "fixture-catalog" {
		t.Fatal("source silently refreshed frozen catalog")
	}
}
func TestCapturedSourceRejectsIncompleteIdentityAndUnsupportedRepresentation(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*CapturedSource)
		operand bool
	}{
		{"digest", func(c *CapturedSource) { c.Pin.SHA256 = "bad" }, false},
		{"revision", func(c *CapturedSource) { c.Pin.Revision = "" }, false},
		{"partial", func(c *CapturedSource) { c.Pin.StartByte = 1 }, false},
		{"range", func(c *CapturedSource) { c.Pin.EndByte++ }, false},
		{"encoding", func(c *CapturedSource) { c.Pin.Representation = toolcontract.OriginalEncodedBytes }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := capturedFixture([]byte(":"))
			tc.change(&c)
			p, _, err := LoadSourceUnit(t.Context(), sourceCaptureFunc(func(context.Context, toolcontract.OperationInvocation, string) (CapturedSource, error) { return c, nil }), sourceInvocation(), "script.pw")
			if p != nil || err == nil {
				t.Fatalf("accepted invalid source pin: %+v", c.Pin)
			}
			var integrity *SourceIntegrityError
			var diagnostic *commandsurface.SyntaxDiagnostic
			if tc.operand {
				if !errors.As(err, &diagnostic) || diagnostic.Code != "COMMAND_OPERAND_INVALID" {
					t.Fatalf("encoding refusal=%v", err)
				}
			} else if !errors.As(err, &integrity) {
				t.Fatalf("integrity refusal=%v", err)
			}
		})
	}
}
func TestSourceCaptureRefusalAndSyntaxFailureNeverReturnPartialPlan(t *testing.T) {
	refusal := errors.New("fixture source authority refused")
	for _, capture := range []SourceCapture{nil, sourceCaptureFunc(func(context.Context, toolcontract.OperationInvocation, string) (CapturedSource, error) {
		return CapturedSource{}, refusal
	}), sourceCaptureFunc(func(context.Context, toolcontract.OperationInvocation, string) (CapturedSource, error) {
		return capturedFixture([]byte("printf changed > ./child; if")), nil
	})} {
		p, _, err := LoadSourceUnit(t.Context(), capture, sourceInvocation(), "script.pw")
		if p != nil || err == nil {
			t.Fatalf("partial source admitted: %#v %v", p, err)
		}
	}
}
