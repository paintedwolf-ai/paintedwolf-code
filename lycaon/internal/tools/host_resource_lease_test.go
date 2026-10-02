package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostresources"
)

func TestSocketMatchesRealization(t *testing.T) {
	grant := confine.SocketGrant{
		ApprovedPath: "/var/run/docker.sock",
		ResolvedPath: "/private/var/run/docker.sock",
	}
	if !socketMatchesRealization(grant, []string{"/var/run/docker.sock"}) {
		t.Fatal("approved path should match the catalogued realization")
	}
	if !socketMatchesRealization(grant, []string{"/private/var/run/docker.sock"}) {
		t.Fatal("resolved path should match the catalogued realization")
	}
	if socketMatchesRealization(grant, []string{"/tmp/other.sock"}) {
		t.Fatal("an unrelated socket is not the realization")
	}
	if socketMatchesRealization(grant, nil) {
		t.Fatal("empty realization must not match")
	}
}

func TestRealizationSocketTargets(t *testing.T) {
	got := realizationSocketTargets(&hostresources.ActionResolution{
		Connections: hostresources.ConnectionRequest{
			LocalServices: []hostresources.LocalServiceEndpoint{
				{Transport: hostresources.LocalServiceUnixSocket, Target: "/var/run/docker.sock"},
				{Transport: hostresources.LocalServiceUnixSocket, Target: "  "},
			},
		},
	})
	if len(got) != 1 || got[0] != "/var/run/docker.sock" {
		t.Fatalf("realization sockets = %v", got)
	}
	if realizationSocketTargets(nil) != nil {
		t.Fatal("nil resolution should yield no sockets")
	}
}

func TestPermitCoveredHostResourceSocketsIssuesRealizationOnly(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "docker.sock")
	extra := filepath.Join(dir, "other.sock")
	rt := &memorySocketRuntime{}
	exec := &DefaultToolExecutor{socketRuntime: rt, approvalGate: coveringHostResourceGate{}}
	docker := confine.SocketGrant{ApprovedPath: sock, ResolvedPath: sock}
	other := confine.SocketGrant{ApprovedPath: extra, ResolvedPath: extra}
	covered, extras := exec.permitCoveredHostResourceSockets(
		context.Background(),
		hitl.ProposedAction{Tool: "command", HostResources: []string{"docker"}},
		"digest-1",
		[]confine.SocketGrant{docker, other},
		ToolContext{
			SessionID:          "sess",
			ToolCallID:         "tc-1",
			RealizationSockets: []string{sock},
		},
	)
	if !covered {
		t.Fatal("covering lease should apply")
	}
	if len(extras) != 1 || extras[0].ApprovedPath != extra {
		t.Fatalf("extras = %+v, want only the uncatalogued socket", extras)
	}
	if len(rt.permits) != 1 {
		t.Fatalf("permits = %d, want the realization socket only", len(rt.permits))
	}
}

type coveringHostResourceGate struct{}

func (coveringHostResourceGate) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	return &hitl.ApprovalResult{}, nil
}
func (coveringHostResourceGate) GrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}
func (coveringHostResourceGate) AbsorbedGrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}
func (coveringHostResourceGate) ApplyGrant(hitl.ApprovalGrant) (bool, error)      { return false, nil }
func (coveringHostResourceGate) GrantCovers(hitl.ProposedAction) bool             { return false }
func (coveringHostResourceGate) HostResourceLeaseCovers(hitl.ProposedAction) bool { return true }
func (coveringHostResourceGate) RevokeGrant(string) (bool, error)                 { return false, nil }
func (coveringHostResourceGate) RevokeGrantInstalledBy(string, string) (bool, error) {
	return false, nil
}
func (coveringHostResourceGate) ListGrants(string) []hitl.ApprovalGrant { return nil }
func (coveringHostResourceGate) SecretFingerprintsCovered(string, string, string, string, []string) bool {
	return false
}
func (coveringHostResourceGate) SecretRedactionStanding(string, []string) bool { return false }
func (coveringHostResourceGate) PutAskQuiet(hitl.AskQuiet, int) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}
func (coveringHostResourceGate) AskQuietLive(string, string) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}
func (coveringHostResourceGate) NoteAskQuietSuppressed(string, string)         {}
func (coveringHostResourceGate) ListAskQuiets(string) []hitl.AskQuiet          { return nil }
func (coveringHostResourceGate) RevokeAskQuiet(string) bool                    { return false }
func (coveringHostResourceGate) RevokeAskQuietInstalledBy(string, string) bool { return false }
func (coveringHostResourceGate) ForgetSession(string)                          {}
