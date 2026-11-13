package oar

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProf10HostRefusesUndeliverableTransform(t *testing.T) {
	for _, anchor := range []string{AnchorToolPreInvoke, AnchorToolHandler, AnchorContentInput, AnchorCoordinatorPostTurn, AnchorCoordinatorCloseoutCheck, AnchorWorkerFinalize, AnchorWorkerReportCheck} {
		if err := ValidateHostRuleSet(NewRuleSet([]*Rule{{ID: "REWRITE", Anchor: anchor, Effect: EffectTransform}})); err == nil {
			t.Errorf("accepted transform at %s", anchor)
		}
	}
	for _, anchor := range []string{AnchorToolPost, AnchorContentOutput, AnchorContentToolResult, AnchorCredentialAssignment} {
		if err := ValidateHostRuleSet(NewRuleSet([]*Rule{{ID: "REWRITE", Anchor: anchor, Effect: EffectTransform}})); err != nil {
			t.Errorf("validate transform boundary %s: %v", anchor, err)
		}
	}
}

func TestFact24HostRejectsUnimplementedCapability(t *testing.T) {
	ensureCatalog(t)
	for _, mutate := range []func(*EngineDeclaration){
		func(p *EngineDeclaration) { p.HostAnchors = append(p.HostAnchors, "unimplemented.anchor") },
		func(p *EngineDeclaration) {
			p.HostFacts = append(p.HostFacts, HostFactDecl{Name: "paintedwolf.imaginary", Type: "bool"})
		},
		func(p *EngineDeclaration) { p.Detectors = append(p.Detectors, "detector://unimplemented") },
	} {
		p, err := LoadEngineDeclaration(hostCapabilityDocumentPath(t))
		if err != nil {
			t.Fatalf("load host capability: %v", err)
		}
		mutate(p)
		if err := p.requireHostAdapters(); err == nil {
			t.Fatal("accepted capability without implementation")
		}
	}
}

func TestFact11ProviderCanSetObservationsAndRunsOnce(t *testing.T) {
	gc := NewGuardContext()
	var calls atomic.Int64
	failure := errors.New("[OAR-FACT-18] provider failure")
	gc.RegisterProvider("paintedwolf.repeat_count", func(gc *GuardContext) error {
		calls.Add(1)
		gc.SetRepeatCount(7)
		return failure
	})
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := gc.Ensure("paintedwolf.repeat_count"); !errors.Is(err, failure) {
				t.Errorf("[OAR-FACT-18] error = %v", err)
			}
		}()
	}
	workers.Wait()
	if calls.Load() != 1 || gc.Counters.RepeatCount != 7 {
		t.Fatalf("[OAR-FACT-9] calls=%d observation=%d", calls.Load(), gc.Counters.RepeatCount)
	}
}
