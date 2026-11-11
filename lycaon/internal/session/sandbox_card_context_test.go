package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type cardContextProvenance struct {
	fixedProvenance
	t     *testing.T
	want  context.Context
	calls int
}

func (p *cardContextProvenance) IsSessionOwned(ctx context.Context, _, _ string, _ uint16) (bool, ProvenanceEvidence) {
	p.t.Helper()
	if ctx != p.want {
		p.t.Error("approval card replaced the invoking operation context")
	}
	p.calls++
	return false, ProvenanceEvidence{}
}

func TestApprovalCardsKeepInvokingContextForHolderObservations(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := &cardContextProvenance{t: t, want: ctx}
	_, err := (&ListenCheckpointBroker{Provenance: p}).buildListenCard(ctx,
		tools.LocalListenAsk{ProjectDir: t.TempDir()}, "child", "root", []uint16{8000})
	testutil.FailErr(t, "build listen card", err)
	_, err = (&LoopbackCheckpointBroker{Provenance: p}).buildCard(ctx,
		tools.LoopbackConnectAsk{ProjectDir: t.TempDir()}, "child", "root", []uint16{8001})
	testutil.FailErr(t, "build loopback card", err)
	_, err = (&LocalNetworkCheckpointBroker{Provenance: p}).buildCard(ctx,
		tools.LocalNetworkAsk{ProjectDir: t.TempDir(), ListenPorts: []uint16{8002}, ConnectPorts: []uint16{8003}}, "child", "root")
	testutil.FailErr(t, "build local network card", err)
	if p.calls != 4 {
		t.Fatalf("observed %d port holders, want all four", p.calls)
	}
}
