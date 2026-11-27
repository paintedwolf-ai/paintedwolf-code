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
	live       []*confine.ActionLease
	settled    confine.SandboxRefusals
	network    []confine.EgressHost
	transports []confine.GitTransportObservation
}

// JobConfinement is a job's confinement facts captured at one moment. A pipeline
// job reads its own spawn; a program job reads the stages it launched.
type JobConfinement struct {
	Boundary confine.Boundary
	Report   confine.Report
	spawn    confine.SpawnFacts
	stages   *programStages
	settled  *confine.Observation
}

// SpawnConfinement is the confinement of a job that spawned its own processes.
func SpawnConfinement(boundary confine.Boundary, facts confine.SpawnFacts) JobConfinement {
	return JobConfinement{Boundary: boundary, Report: facts.Report, spawn: facts}
}

// Refusals is what the kernel has refused the job so far.
func (c JobConfinement) Refusals() confine.SandboxRefusals {
	if c.stages == nil {
		return c.spawn.Refusals()
	}
	out := c.stages.settled
	if c.settled != nil {
		out = out.Merge(c.settled.Refusals)
	}
	return out
}

// SettledRefusals waits for an ended pipeline's last reports; program stages
// settle before their job ends.
func (c JobConfinement) SettledRefusals(ctx context.Context) confine.SandboxRefusals {
	if c.stages == nil {
		return c.spawn.SettledRefusals(ctx)
	}
	return c.Refusals()
}

// Network is every mediated destination the job's processes reached.
func (c JobConfinement) Network() []confine.EgressHost {
	if c.stages == nil {
		return c.spawn.MediatedNetwork()
	}
	return append([]confine.EgressHost(nil), c.stages.network...)
}

// GitTransports returns verified transport mediation observations recorded across stages.
func (c JobConfinement) GitTransports() []confine.GitTransportObservation {
	var out []confine.GitTransportObservation
	if c.settled != nil {
		for _, tr := range c.settled.GitTransports {
			out = append(out, tr.Clone())
		}
	}
	if c.stages != nil {
		for _, tr := range c.stages.transports {
			if !slices.ContainsFunc(out, func(existing confine.GitTransportObservation) bool {
				return existing.ExecutionID != "" && existing.ExecutionID == tr.ExecutionID
			}) {
				out = append(out, tr.Clone())
			}
		}
	}
	return out
}

// MergedObservation combines stage-level stamped refusal facts with any settled root program observation.
func (c JobConfinement) MergedObservation(stamped confine.Observation) confine.Observation {
	if c.settled == nil {
		return stamped
	}
	return c.settled.Merge(stamped)
}

// confinementLocked captures proc's confinement. Callers hold the table lock.
func (proc *Process) confinementLocked() JobConfinement {
	if proc.kind != processKindProgram {
		return SpawnConfinement(proc.boundary, proc.facts)
	}
	stages := &programStages{
		settled:    proc.program.settled,
		network:    append([]confine.EgressHost(nil), proc.program.network...),
		transports: make([]confine.GitTransportObservation, len(proc.program.transports)),
	}
	for i, tr := range proc.program.transports {
		stages.transports[i] = tr.Clone()
	}
	for _, lease := range proc.program.live {
		stages.settled = stages.settled.Merge(lease.Refusals())
	}
	var settledCopy *confine.Observation
	if proc.settledObservation != nil {
		obs := proc.settledObservation.Merge(confine.Observation{Applied: proc.settledObservation.Applied})
		obs.Running = proc.running
		settledCopy = &obs
	}
	return JobConfinement{Boundary: proc.boundary, Report: proc.facts.Report, stages: stages, settled: settledCopy}
}

// AttachProgramStage counts an external stage's refusals toward the program
// job that launched it. settle records the stage's final reports once its
// process exits; until then the live lease is read. Only the first settle counts.
func (r *Registry) AttachProgramStage(ctx context.Context, sessionID, handle string, stage ProgramStage) (settle func(confine.SandboxRefusals, []confine.EgressHost, ...confine.GitTransportObservation)) {
	none := func(confine.SandboxRefusals, []confine.EgressHost, ...confine.GitTransportObservation) {}
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
	return func(refusals confine.SandboxRefusals, network []confine.EgressHost, transports ...confine.GitTransportObservation) {
		r.jobs.mu.Lock()
		defer r.jobs.mu.Unlock()
		i := slices.Index(proc.program.live, stage.Lease)
		if i < 0 {
			return
		}
		proc.program.live = slices.Delete(proc.program.live, i, i+1)
		proc.program.settled = proc.program.settled.Merge(refusals)
		proc.program.network = append(proc.program.network, network...)
		for _, tr := range transports {
			proc.program.transports = append(proc.program.transports, tr.Clone())
		}
	}
}
