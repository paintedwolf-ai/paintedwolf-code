package terminal

import (
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/tools"
)

// observation captures the boundary applied to one terminal.
type observation struct {
	Boundary confine.Boundary
	Report   confine.Report
	Network  []confine.EgressHost
	Refusals confine.SandboxRefusals
	Running  bool
}

func snapshotObservation(res bgprocess.PTYSnapshotResult) observation {
	return observation{
		Boundary: res.Boundary, Report: res.Report, Network: res.Network,
		Refusals: res.Refusals, Running: res.Running,
	}
}

func readObservation(res bgprocess.PTYReadResult) observation {
	return observation{
		Boundary: res.Boundary, Report: res.Report, Network: res.Network,
		Refusals: res.Refusals, Running: res.Running,
	}
}

func closeObservation(res bgprocess.PTYCloseResult) observation {
	return observation{
		Boundary: res.Boundary, Report: res.Report, Network: res.Network,
		Refusals: res.Refusals,
	}
}

// stampBoundary records refusal facts from one terminal observation.
func stampBoundary(tctx tools.ToolContext, tool string, obs observation) confine.Report {
	report := obs.Report
	report.Confined = obs.Boundary.Applied
	if !obs.Boundary.Applied {
		return report
	}
	stamped := confine.StampRefusal(
		tool,
		tctx.SessionID,
		obs.Boundary,
		confine.RefusalContext{
			MediatedNetwork:        obs.Network,
			RemotePackageExecution: report.RemotePackageExecution,
			Running:                obs.Running,
			Refusals:               obs.Refusals,
		},
	)
	report.BoundaryRefusal = string(stamped.Attribution)
	if tctx.Out != nil {
		tctx.Out.Facts = tools.ApplyRefusalFacts(tctx.Out.Facts, stamped)
	}
	return report.WithSandboxRefusals(obs.Refusals)
}
