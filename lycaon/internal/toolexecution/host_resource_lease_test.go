package toolexecution

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostresources"
)

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
