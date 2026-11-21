package failure_test

import (
	"errors"
	"net"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/failure"
)

// TestProviderFaultClassifiersAreDistinct requires each classifier to accept
// its own typed fault, bare and wrapped, and to reject the fault's text alone
// and every other fault, so the notice rail can tell them apart.
func TestProviderFaultClassifiersAreDistinct(t *testing.T) {
	cases := []struct {
		name       string
		fault      error
		classified func(error) bool
	}{
		{"rate_limited", &failure.ProviderRateLimitedError{ProviderID: "mock", Model: "mock-1", Status: 429, Detail: "slow down"},
			func(err error) bool { _, ok := failure.AsProviderRateLimited(err); return ok }},
		{"overloaded", &failure.ProviderOverloadedError{ProviderID: "mock", Model: "mock-1", Status: 503, Detail: "engine overloaded"},
			func(err error) bool { _, ok := failure.AsProviderOverloaded(err); return ok }},
		{"context_too_small", &failure.ProviderContextTooSmallError{ProviderID: "mock", Model: "mock-1", PromptTokens: 200_000, MaxContext: 8_192},
			func(err error) bool { _, ok := failure.AsProviderContextTooSmall(err); return ok }},
		{"empty_completion", &failure.ProviderEmptyCompletionError{ProviderID: "mock", Model: "mock-1"},
			func(err error) bool { _, ok := failure.AsProviderEmptyCompletion(err); return ok }},
		{"unreachable", &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}, httpclient.Unreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.classified(tc.fault) {
				t.Fatalf("%s does not classify its own fault %v", tc.name, tc.fault)
			}
			if !tc.classified(errors.Join(errors.New("turn 3"), tc.fault)) {
				t.Fatalf("%s classifier does not survive wrapping", tc.name)
			}
			if tc.classified(errors.New("turn 3: " + tc.fault.Error())) {
				t.Fatalf("%s classifier accepts diagnostic text without a typed cause", tc.name)
			}
			for _, other := range cases {
				if other.name != tc.name && tc.classified(other.fault) {
					t.Fatalf("%s classifier also matches %s", tc.name, other.name)
				}
			}
		})
	}
}
