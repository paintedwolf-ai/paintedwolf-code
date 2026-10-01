package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

type namingProvider struct {
	namedStubProvider
	budgets []int
	failure error
}

func (p *namingProvider) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.budgets = append(p.budgets, req.MaxTokens)
	if len(p.budgets) == 1 {
		return nil, p.failure
	}
	return &modelcall.Completion{Content: "Inspect source indexing"}, nil
}

func TestNamingRecoversOnlyStructuredOutputExhaustion(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		retries bool
	}{
		"reasoning consumed output": {&failure.ProviderEmptyCompletionError{Terminal: true, Reason: "length"}, true},
		"truncated output":          {&failure.ProviderOutputTruncatedError{}, true},
		"ordinary empty response":   {&failure.ProviderEmptyCompletionError{Terminal: true, Reason: "stop"}, false},
		"unstructured message":      {errors.New("length max_tokens"), false},
		"cancellation":              {context.Canceled, false},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &namingProvider{namedStubProvider: namedStubProvider{id: "lite"}, failure: tc.err}
			coordinator := &namedStubProvider{id: "coord"}
			summarizer := newFallbackTestSummarizer(t, provider, coordinator)
			summarizer.Plane = NewUtilityPlane()
			title, err := (PlaneNamer{Summarizer: summarizer}).Name(t.Context(), "Name the task", "Review indexing")
			if len(provider.budgets) == 0 || provider.budgets[0] < 1024 {
				t.Fatalf("reasoning has no completion allowance: %v", provider.budgets)
			}
			if tc.retries {
				if err != nil || title != "Inspect source indexing" || len(provider.budgets) != 2 || provider.budgets[1] <= provider.budgets[0] {
					t.Fatalf("recovery title=%q error=%v budgets=%v", title, err, provider.budgets)
				}
			} else if err == nil || len(provider.budgets) != 1 {
				t.Fatalf("unexpected replay: title=%q error=%v budgets=%v", title, err, provider.budgets)
			}
			if coordinator.calls != 0 {
				t.Fatal("naming spent coordinator capacity")
			}
		})
	}
}
