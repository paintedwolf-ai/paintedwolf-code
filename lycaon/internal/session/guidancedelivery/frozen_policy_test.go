package guidancedelivery

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func frozenFeedbackService(t *testing.T) *Service {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load feedback registry", err)
	return &Service{kicks: &kick.KickEngine{}, renderer: oar.NewRenderer(guidance.NewStaticRejectFormatter(hints), nil)}
}
func TestCopy1QueuesFrozenExtensionAdvisories(t *testing.T) {
	svc := frozenFeedbackService(t)
	decision := &oar.Decision{Effect: oar.EffectNudge, Code: "example/EXTENSION", Advisories: []oar.Advisory{
		{Code: "example/EXTENSION", Copy: map[string]string{"what": "Frozen extension copy"}},
	}}
	testutil.FailErr(t, "queue frozen advisory", svc.queuePolicy(t.Context(), "session", oar.AnchorCoordinatorPostTurn, decision))
	lease := svc.kicks.LeasePolicyFeedback("session")
	if len(lease.Entries) != 1 {
		t.Fatalf("queued entries = %d", len(lease.Entries))
	}
	message, err := svc.policyMessage(t.Context(), lease)
	testutil.FailErr(t, "render queued feedback", err)
	text := message.Content
	if !strings.Contains(text, "Frozen extension copy") || !strings.Contains(text, "example/EXTENSION") {
		t.Fatalf("[OAR-COPY-1] extension advisory lost its rendered copy or identity: %q", text)
	}
}

func TestEval20QueuesAllNamespacedAdvisoriesInOneDelivery(t *testing.T) {
	svc := frozenFeedbackService(t)
	decision := &oar.Decision{Effect: oar.EffectWarn, Code: "SHARED"}
	for i := range 40 {
		decision.Advisories = append(decision.Advisories, oar.Advisory{
			Code: "SHARED", Rule: fmt.Sprintf("namespace.r%d/SHARED", i),
			Copy: map[string]string{"what": fmt.Sprintf("Frozen advisory %02d", i)},
		})
	}
	testutil.FailErr(t, "queue namespaced advisories", svc.queuePolicy(t.Context(), "session", oar.AnchorContentOutput, decision))
	lease := svc.kicks.LeasePolicyFeedback("session")
	message, err := svc.policyMessage(t.Context(), lease)
	testutil.FailErr(t, "render queued feedback", err)
	text := message.Content
	ok := len(lease.Entries) == 40
	if !ok || strings.Count(text, "Code: SHARED") != 40 {
		t.Fatalf("[OAR-EVAL-20] advisories collided or exceeded the queue: %q, ok=%v", text, ok)
	}
	previous := -1
	for i := range 40 {
		index := strings.Index(text, fmt.Sprintf("Frozen advisory %02d", i))
		if index <= previous {
			t.Fatalf("[OAR-EVAL-20] advisory %d missing or out of order: %q", i, text)
		}
		previous = index
	}
}
