package llm

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestBindSummarizerForwardsServiceLifecycle(t *testing.T) {
	life := &recordingLifecycle{}
	svc := &Service{Lifecycle: life}
	sum := svc.BindSummarizer(&RegistrySummarizer{})
	if sum.Lifecycle == nil {
		t.Fatal("BindSummarizer left Lifecycle nil")
	}
	sum.Lifecycle.PublishCall(occupiedSessionCtx(), api.LLMCallEvent{Status: api.LLMCallStatusActive})
	if len(life.evs) != 1 || life.evs[0].Status != api.LLMCallStatusActive {
		t.Fatalf("forwarded events = %+v", life.evs)
	}
}
