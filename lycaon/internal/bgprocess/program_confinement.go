package bgprocess

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/confine"
)

// ProgramStage is the confinement of one external process a program job launched.
type ProgramStage struct {
	Boundary confine.Boundary
	Report   confine.Report
	Lease    *confine.ActionLease
}

// programStages gathers a program job's stage confinement: live leases while
// their processes run, then each stage's settled reports.
type programStages struct {
	live    []*confine.ActionLease
	settled confine.SandboxRefusals
	network []confine.EgressHost
}

// Confinement is a job's confinement facts captured at one moment. A pipeline
// job reads its own spawn; a program job reads the stages it launched.
type Confinement struct {
	Boundary confine.Boundary
	Report   confine.Report
	spawn    confine.SpawnFacts
	stages   *programStages
}

// SpawnConfinement is the confinement of a job that spawned its own processes.
func SpawnConfinement(boundary confine.Boundary, facts confine.SpawnFacts) Confinement {
	return Confinement{Boundary: boundary, Report: facts.Report, spawn: facts}
}

// Refusals is what the kernel has refused the job so far.
func (c Confinement) Refusals() confine.SandboxRefusals {
	if c.stages == nil {
		return c.spawn.Refusals()
	}
	return c.stages.settled
}

// SettledRefusals waits for an ended pipeline's last reports; program stages
// settle before their job ends.
func (c Confinement) SettledRefusals(ctx context.Context) confine.SandboxRefusals {
	if c.stages == nil {
		return c.spawn.SettledRefusals(ctx)
	}
	return c.stages.settled
}

// Network is every mediated destination the job's processes reached.
func (c Confinement) Network() []confine.EgressHost {
	if c.stages == nil {
		return c.spawn.MediatedNetwork()
	}
	return append([]confine.EgressHost(nil), c.stages.network...)
}

// confinementLocked captures proc's confinement. Callers hold the table lock.
func (proc *Process) confinementLocked() Confinement {
	if proc.kind != processKindProgram {
		return SpawnConfinement(proc.boundary, proc.facts)
	}
	stages := &programStages{settled: proc.program.settled, network: append([]confine.EgressHost(nil), proc.program.network...)}
	for _, lease := range proc.program.live {
		stages.settled = stages.settled.Merge(lease.Refusals())
	}
	return Confinement{Boundary: proc.boundary, Report: proc.facts.Report, stages: stages}
}

// AttachProgramStage counts an external stage's refusals toward the program
// job that launched it. settle records the stage's final reports once its
// process exits; until then the live lease is read. Only the first settle counts.
func (r *Registry) AttachProgramStage(ctx context.Context, sessionID, handle string, stage ProgramStage) (settle func(confine.SandboxRefusals, []confine.EgressHost)) {
	none := func(confine.SandboxRefusals, []confine.EgressHost) {}
	if r == nil || stage.Lease == nil {
		return none
	}
	proc, err := r.jobs.lookup(sessionID, handle)
	if err != nil || proc.kind != processKindProgram {
		return none
	}
	r.jobs.mu.Lock()
	if !proc.running {
		r.jobs.mu.Unlock()
		return none
	}
	if !proc.boundary.Applied && stage.Boundary.Applied {
		proc.boundary, proc.facts.Report = stage.Boundary, stage.Report
	}
	proc.program.live = append(proc.program.live, stage.Lease)
	r.jobs.mu.Unlock()
	stage.Lease.OnRefusal(func() { r.Output.refusalObserved(ctx, proc) })
	return func(refusals confine.SandboxRefusals, network []confine.EgressHost) {
		r.jobs.mu.Lock()
		defer r.jobs.mu.Unlock()
		i := slices.Index(proc.program.live, stage.Lease)
		if i < 0 {
			return
		}
		proc.program.live = slices.Delete(proc.program.live, i, i+1)
		proc.program.settled = proc.program.settled.Merge(refusals)
		proc.program.network = append(proc.program.network, network...)
	}
}
