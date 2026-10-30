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

type sourceCaptureFunc func(context.Context, toolcontract.OperationInvocation, SourceCaptureRequest) (CapturedSource, error)

func (f sourceCaptureFunc) Capture(ctx context.Context, in toolcontract.OperationInvocation, request SourceCaptureRequest) (CapturedSource, error) {
	return f(ctx, in, request)
}
func capturedFixture(raw []byte) CapturedSource {
	hash := sha256.Sum256(raw)
	return CapturedSource{Bytes: raw, Pin: toolcontract.SourcePin{SourceID: "fixture-source", Revision: "revision-2", Representation: toolcontract.UTF8SourceBytes, SHA256: hex.EncodeToString(hash[:]), EndByte: int64(len(raw))}}
}
func sourceInvocation() toolcontract.OperationInvocation {
	return toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program-1", NodeID: "source-node", ExecutionID: "execution-2"}, ActorID: "fixture-actor", SessionID: "fixture-session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "fixture-project"}, SourceUnitID: "parent-unit", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
}
func TestCapturedSourceUnitUsesExactOwnerBytesAndPreservesIdentity(t *testing.T) {
	raw := []byte("printf '%s' \"$1\"\n")
	capture := sourceCaptureFunc(func(ctx context.Context, in toolcontract.OperationInvocation, request SourceCaptureRequest) (CapturedSource, error) {
		if request.Path != "./script.pw" || request.MaximumBytes != 1<<20 || request.Representation != toolcontract.UTF8SourceBytes || in.Identity.ExecutionID != "execution-2" {
			t.Fatalf("capture changed invocation: %+v %+v", request, in)
		}
		return capturedFixture(raw), nil
	})
	p, captured, err := LoadSourceUnit(t.Context(), capture, sourceInvocation(), "./script.pw", fixtureScratch(t))
	testutil.FailErr(t, "capture and validate source", err)
	defer p.Close()
	raw[0] = 'x'
	if string(p.Source()) != "printf '%s' \"$1\"\n" || captured.Pin.Revision != "revision-2" || p.Snapshot().SourceDigest != captured.Pin.SHA256 {
		t.Fatalf("captured different source: %+v", captured.Pin)
	}
	captured.Bytes[0] = 'y'
	if p.Source()[0] != 'p' {
		t.Fatal("retained parser bytes were mutable through capture")
	}
	if p.Snapshot().CatalogDigest != toolcontract.LanguageCatalogDigest() {
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
			p, _, err := LoadSourceUnit(t.Context(), sourceCaptureFunc(func(context.Context, toolcontract.OperationInvocation, SourceCaptureRequest) (CapturedSource, error) {
				return c, nil
			}), sourceInvocation(), "script.pw", fixtureScratch(t))
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
	for _, capture := range []SourceCapture{nil, sourceCaptureFunc(func(context.Context, toolcontract.OperationInvocation, SourceCaptureRequest) (CapturedSource, error) {
		return CapturedSource{}, refusal
	}), sourceCaptureFunc(func(context.Context, toolcontract.OperationInvocation, SourceCaptureRequest) (CapturedSource, error) {
		return capturedFixture([]byte("printf changed > ./child; if")), nil
	})} {
		p, _, err := LoadSourceUnit(t.Context(), capture, sourceInvocation(), "script.pw", fixtureScratch(t))
		if p != nil || err == nil {
			t.Fatalf("partial source admitted: %#v %v", p, err)
		}
	}
}

func fixtureScratch(t *testing.T) *toolcontract.ProgramMemory {
	t.Helper()
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "load catalog program memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "create program memory", err)
	return memory
}

type missingCaptureLease struct{ *toolcontract.ProgramMemory }

func (m missingCaptureLease) ReserveProgramState(context.Context, int64) (func(), error) {
	return nil, nil
}

func TestSourceCaptureRequiresWorkingAndBufferAdmissionBeforeCapture(t *testing.T) {
	for _, account := range []string{"working", "state", "missing state lease"} {
		t.Run(account, func(t *testing.T) {
			memory := fixtureScratch(t)
			limits, err := toolcontract.CatalogProgramMemoryLimits()
			testutil.FailErr(t, "load program bounds", err)
			var release func()
			var admission commandsurface.ProgramMemoryAdmission = memory
			switch account {
			case "working":
				release, err = memory.Reserve(t.Context(), limits.Working-1024)
			case "state":
				release, err = memory.ReserveProgramState(t.Context(), limits.State-(1<<20))
			default:
				admission = missingCaptureLease{memory}
			}
			testutil.FailErr(t, "hold existing program memory", err)
			if release != nil {
				defer release()
			}
			before := memory.InUse()
			calls := 0
			capture := sourceCaptureFunc(func(context.Context, toolcontract.OperationInvocation, SourceCaptureRequest) (CapturedSource, error) {
				calls++
				return capturedFixture([]byte(":")), nil
			})
			parsed, captured, err := LoadSourceUnit(t.Context(), capture, sourceInvocation(), "script.pw", admission)
			if parsed != nil || len(captured.Bytes) != 0 || err == nil || calls != 0 {
				t.Fatalf("capture ran without admission: calls=%d parsed=%v err=%v", calls, parsed, err)
			}
			if memory.InUse() != before {
				t.Fatalf("failed pre-capture admission leaked memory: %+v before=%+v", memory.InUse(), before)
			}
		})
	}
}

func TestSourceCaptureBuffersTransferToParsedAndSnapshotState(t *testing.T) {
	memory := fixtureScratch(t)
	held, err := memory.Reserve(t.Context(), 16<<20)
	testutil.FailErr(t, "hold concurrent parser work", err)
	defer held()
	capture := sourceCaptureFunc(func(_ context.Context, _ toolcontract.OperationInvocation, request SourceCaptureRequest) (CapturedSource, error) {
		usage := memory.InUse()
		if usage.State != 2*request.MaximumBytes || usage.Working <= 16<<20 {
			t.Fatalf("capture not bounded before materialization: %+v request=%+v", usage, request)
		}
		return capturedFixture([]byte("printf sourced")), nil
	})
	parsed, captured, err := LoadSourceUnit(t.Context(), capture, sourceInvocation(), "script.pw", memory)
	testutil.FailErr(t, "capture source with bounded overlap", err)
	defer parsed.Close()
	if usage := memory.InUse(); usage.Working != 16<<20 || usage.State < int64(len(captured.Bytes)) || usage.State >= 2*commandsurface.SyntaxUnitByteLimit(true) {
		t.Fatalf("temporary capacity did not transfer to retained envelope: %+v", usage)
	}
	snapshot, err := parsed.SnapshotWithMemory(t.Context(), memory)
	testutil.FailErr(t, "retain source execution snapshot", err)
	testutil.FailErr(t, "release parsed source", parsed.Close())
	if string(captured.Bytes) != "printf sourced" || memory.InUse().State < int64(len(captured.Bytes)) {
		t.Fatal("captured bytes lost their snapshot coverage")
	}
	testutil.FailErr(t, "release source execution snapshot", snapshot.Close())
	if memory.InUse().State != 0 {
		t.Fatalf("source execution retained state: %+v", memory.InUse())
	}
}

func TestSourceCaptureErrorsReleaseCapacityWithoutReturningBytes(t *testing.T) {
	refused := errors.New("capture refused")
	for _, tc := range []struct {
		name   string
		source CapturedSource
		err    error
	}{
		{"producer", CapturedSource{}, refused},
		{"syntax", capturedFixture([]byte("if")), nil},
		{"identity", CapturedSource{Bytes: []byte(":")}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memory := fixtureScratch(t)
			parsed, captured, err := LoadSourceUnit(t.Context(), sourceCaptureFunc(func(context.Context, toolcontract.OperationInvocation, SourceCaptureRequest) (CapturedSource, error) {
				return tc.source, tc.err
			}), sourceInvocation(), "script.pw", memory)
			if parsed != nil || err == nil || len(captured.Bytes) != 0 {
				t.Fatalf("error returned live unleased source: %v %+v %v", parsed, captured, err)
			}
			if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
				t.Fatalf("capture error leaked capacity: %+v", memory.InUse())
			}
		})
	}
}
