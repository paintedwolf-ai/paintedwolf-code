package bgprocess

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestExecutionProtectionTracksBoundaryAndObservedExit(t *testing.T) {
	r := NewRegistry(Config{}, Hooks{})
	r.jobs.sessions["task"] = map[string]*Process{
		"cred":     {Handle: "cred", running: true, boundary: confine.Boundary{CredentialStore: true}},
		"host":     {Handle: "host", running: true, boundary: confine.Boundary{HostExecution: true, Network: confine.NetworkDirectIP}},
		"signal":   {Handle: "signal", livenessUnknown: true, boundary: confine.Boundary{ProcessControl: true}},
		"done":     {Handle: "done", hasExit: true, boundary: confine.Boundary{HostExecution: true}},
		"ordinary": {Handle: "ordinary", running: true},
	}
	jobs := r.ActiveExecutionJobs("task")
	if len(jobs) != 3 || jobs[0].Handle != "cred" || jobs[0].Capability != "credential_store" || jobs[0].Status != "active" || jobs[1].Handle != "host" || jobs[1].Capability != "host_execution" || jobs[1].Status != "active" || jobs[2].Status != "unknown" {
		t.Fatalf("incorrect live authority: %+v", jobs)
	}
	if len(r.ActiveDirectIPJobs("task")) != 0 {
		t.Fatal("host execution mislabeled as direct IP")
	}
	if len(r.ActiveExecutionJobs("other")) != 0 {
		t.Fatal("cross-task visibility")
	}
}
