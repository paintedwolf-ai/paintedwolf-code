// Package commandsurfacetest admits parser fixtures through the runtime budget.
package commandsurfacetest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type AdmittedProgram struct {
	*commandsurface.ParsedProgram
	memory *toolcontract.ProgramMemory
}

func Admit(t testing.TB, source []byte, options commandsurface.ParseOptions) (*AdmittedProgram, error) {
	t.Helper()
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "command memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "command memory account", err)
	program, err := commandsurface.ParseAdmitted(t.Context(), memory, source, options)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = program.Close() })
	return &AdmittedProgram{ParsedProgram: program, memory: memory}, nil
}

func (p *AdmittedProgram) View(t testing.TB) commandsurface.ProgramSnapshot {
	t.Helper()
	lease, err := p.SnapshotWithMemory(t.Context(), p.memory)
	testutil.FailErr(t, "retain program snapshot", err)
	t.Cleanup(func() { _ = lease.Close() })
	return lease.Plan
}
