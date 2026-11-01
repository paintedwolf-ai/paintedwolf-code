package commandprogram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// eval parses its expanded arguments as a new syntax unit. The same admission
// and per-operation dispatch used for top-level programs applies to its body.
func (f *frame) eval(ctx context.Context, s commandsurface.Statement, fields Fields) (signal, error) {
	if int64(f.sources+1) > f.e.limits["source_depth"] {
		return signal{}, f.refuseLimit(s.SyntaxLocation, "source_depth", int64(f.sources+1))
	}
	run, err := f.beginRecorded(ctx, s.SyntaxLocation, "eval")
	if err != nil {
		return signal{}, err
	}
	defer run.close(ctx)
	source := strings.Join(fields.Values()[1:], " ")
	unitID := "eval:" + uuid.NewString()
	parsed, err := commandsurface.ParseAdmitted(ctx, f.e.ports.Memory, []byte(source), commandsurface.ParseOptions{
		SourceName: unitID, Sourced: true, Version: toolcontract.LanguageVersion, CatalogDigest: run.inv.CatalogDigest,
	})
	if err != nil {
		return signal{}, f.sourceRefusal(ctx, run, s, "eval", err)
	}
	lease, err := parsed.SnapshotWithMemory(ctx, f.e.ports.Memory)
	_ = parsed.Close()
	if err != nil {
		run.settle(ctx, ownerResult(err), err)
		return signal{}, f.stateRefusal(s.SyntaxLocation, err)
	}
	defer func() { _ = lease.Close() }()
	if err := preflight(lease.Plan.Statements, f.e.ports); err != nil {
		run.settle(ctx, toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: "COMMAND_SYNTAX_UNSUPPORTED", EffectCertainty: toolcontract.NoEffect}, err)
		var refusal *EvaluationRefusal
		if errors.As(err, &refusal) {
			return signal{}, f.refuse(refusal.Node, refusal.Code, refusal.Expected, nil)
		}
		return signal{}, err
	}
	if registrar, ok := f.e.ports.Executions.(SourceUnitRegistrar); ok {
		sum := sha256.Sum256([]byte(source))
		pin := toolcontract.SourcePin{SourceID: unitID, Revision: hex.EncodeToString(sum[:]), Representation: toolcontract.UTF8SourceBytes, SHA256: hex.EncodeToString(sum[:]), EndByte: int64(len(source))}
		if err := registrar.RegisterSourceUnit(ctx, run.inv, CapturedSource{Pin: pin, Bytes: []byte(source)}, lease.Plan); err != nil {
			run.settle(ctx, ownerResult(err), err)
			return signal{}, err
		}
	}
	g := *f
	g.parent, g.unit, g.sources = run.inv, unitID, f.sources+1
	g.parent.SourceUnitID = unitID
	sig, err := g.statements(ctx, lease.Plan.Statements)
	result := ordinaryResult(f.state.control.Status())
	if stop, ok := f.state.latch.Stop(); ok {
		result = stop.Result
	}
	run.settle(ctx, result, err)
	return sig, err
}
