package command_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

type heldProbe struct {
	invoked, rechecked int
	settled            []string
	stale              error
}

func (p *heldProbe) effect(id string) command.HeldEffect {
	return command.HeldEffect{
		Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: id},
		Recheck:  func(context.Context) error { p.rechecked++; return p.stale },
		Invoke: func(context.Context) (toolcontract.ProducerResult, error) {
			p.invoked++
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
		},
		Settle: func(_ context.Context, result toolcontract.ProducerResult, code string) error {
			p.settled = append(p.settled, string(result.Terminal)+":"+code)
			return nil
		},
	}
}

func TestHeldEffectResumesExactlyOnceAfterFreshRecheck(t *testing.T) {
	probe := &heldProbe{}
	held := &command.ProgramHeldEffects{}
	effect := probe.effect("held-1")
	if err := held.Park(effect); err != nil {
		t.Fatal(err)
	}
	if err := held.Park(effect); err == nil {
		t.Fatal("same execution parked twice")
	}
	result, err := held.Resume(t.Context(), effect.Identity)
	if err != nil || result.EffectCertainty != toolcontract.EffectCompleted || probe.invoked != 1 || probe.rechecked != 1 {
		t.Fatalf("resume %+v %v probe %+v", result, err, probe)
	}
	if _, err := held.Resume(t.Context(), effect.Identity); toolrejection.AsToolReject(err) == nil || probe.invoked != 1 {
		t.Fatalf("held effect replayed: %v invoked %d", err, probe.invoked)
	}
	other := effect.Identity
	other.NodeID = "different-node"
	if _, err := held.Resume(t.Context(), other); toolrejection.AsToolReject(err) == nil {
		t.Fatal("mismatched identity resumed a held effect")
	}
	if len(probe.settled) != 1 || probe.settled[0] != "completed:" {
		t.Fatalf("settlements %v", probe.settled)
	}
}

func TestHeldEffectStaleInputConflictsWithoutRecomputation(t *testing.T) {
	probe := &heldProbe{stale: errors.New("destination revision changed")}
	held := &command.ProgramHeldEffects{}
	effect := probe.effect("held-stale")
	if err := held.Park(effect); err != nil {
		t.Fatal(err)
	}
	result, err := held.Resume(t.Context(), effect.Identity)
	if err == nil || result.Terminal != toolcontract.ProducerRefused || result.StopCode != "EDIT_TARGET_CHANGED_BY_OTHERS" || probe.invoked != 0 {
		t.Fatalf("stale resume %+v %v invoked %d", result, err, probe.invoked)
	}
	if len(probe.settled) != 1 || probe.settled[0] != "refused:EDIT_TARGET_CHANGED_BY_OTHERS" {
		t.Fatalf("stale settlement %v", probe.settled)
	}
}

func TestHeldEffectDenialSettlesWithoutInvoking(t *testing.T) {
	probe := &heldProbe{}
	held := &command.ProgramHeldEffects{}
	effect := probe.effect("held-denied")
	if err := held.Park(effect); err != nil {
		t.Fatal(err)
	}
	if err := held.Stop(t.Context(), effect.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Resume(t.Context(), effect.Identity); err == nil || probe.invoked != 0 || probe.rechecked != 0 {
		t.Fatal("denied effect resumed")
	}
	if err := held.Stop(t.Context(), effect.Identity); err == nil {
		t.Fatal("repeated stop settled twice")
	}
	if len(probe.settled) != 1 || probe.settled[0] != "refused:"+approvaloutcome.CodeApprovalDenied {
		t.Fatalf("denial settlement %v", probe.settled)
	}
	if err := held.Park(command.HeldEffect{}); err == nil {
		t.Fatal("incomplete held effect parked")
	}
}

func TestSupervisorStopAcknowledgesBeforeExitAndClosesAdmission(t *testing.T) {
	supervisor := &command.ProgramSupervisor{}
	root := toolcontract.OperationInvocation{SessionID: "s1", Identity: toolcontract.ExecutionIdentity{ProgramID: "p1", ExecutionID: "root"}}
	entered, release := make(chan struct{}), make(chan struct{})
	var admitted error
	done := make(chan toolcontract.ProducerResult, 1)
	go func() {
		result, _ := supervisor.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			if err := supervisor.AdmitChild(ctx); err != nil {
				t.Errorf("first child refused: %v", err)
			}
			close(entered)
			<-release
			admitted = supervisor.AdmitChild(ctx)
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted}, nil
		})
		done <- result
	}()
	<-entered
	if ids := supervisor.Stop("s1"); len(ids) != 1 || ids[0] != "p1" {
		t.Fatalf("stop acknowledged %v", ids)
	}
	if ids := supervisor.Stop("s1"); len(ids) != 0 {
		t.Fatalf("repeated stop re-acknowledged %v", ids)
	}
	if ids := supervisor.Stop("other-session"); len(ids) != 0 {
		t.Fatal("stop reached another session's program")
	}
	close(release)
	result := <-done
	if admitted == nil || result.Terminal != toolcontract.ProducerInterrupted || result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("stopped program admitted=%v result=%+v", admitted, result)
	}
	if err := supervisor.AdmitChild(context.Background()); err == nil {
		t.Fatal("unsupervised child admitted")
	}
	if _, err := supervisor.RunProgram(t.Context(), toolcontract.OperationInvocation{}, nil); err == nil {
		t.Fatal("program without identity supervised")
	}
	if _, err := supervisor.Lost(t.Context(), "s1", "output:lost"); err == nil {
		t.Fatal("loss reconciled without a receipt owner")
	}
}
