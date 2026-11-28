package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestModelSecretScreenTrackAndReplaceRefreshesScreeningValuesOnWire(t *testing.T) {
	t.Parallel()

	const refMarker = "{{paintedwolf-secret:cccccccc-cccc-4ccc-8ccc-cccccccccccc}}"
	m := modelScreenMatcher(t)

	var (
		mu            sync.Mutex
		vaulted       []secretmatch.Remembered
		evidenceCalls int
		adoptCalls    []ManagedSecretAdoptRequest
	)

	// Mirror production durable evidence: once vaulted, HarvestSource reports
	// the durable non-disclosable secret WITHOUT a reference token, relying on
	// request-scoped screening values for reference substitution.
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		mu.Lock()
		defer mu.Unlock()
		out := make([]secretmatch.HarvestedValue, 0, len(vaulted))
		for _, v := range vaulted {
			out = append(out, secretmatch.HarvestedValue{
				Name:           v.Name,
				Container:      v.Origin,
				Secret:         v.Secret,
				RuleID:         v.RuleID,
				Title:          v.Title,
				Source:         v.Source,
				NonDisclosable: true,
			})
		}
		return out
	})

	screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.TrackAndReplace}, nil
	})
	screen.SetManagedSecretEvidence(func(_ context.Context, projectID, rootSessionID string) ([]secretmatch.Remembered, error) {
		mu.Lock()
		defer mu.Unlock()
		evidenceCalls++
		if projectID != "proj-1" || rootSessionID != "sess-protect" {
			return nil, errors.New("unexpected evidence scope")
		}
		out := make([]secretmatch.Remembered, len(vaulted))
		copy(out, vaulted)
		return out, nil
	})
	screen.SetManagedSecretAdopter(func(_ context.Context, req ManagedSecretAdoptRequest) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		adoptCalls = append(adoptCalls, req)
		vaulted = append(vaulted, secretmatch.Remembered{
			Secret:         req.Value,
			Name:           req.Name,
			Origin:         "managed secret",
			RuleID:         secretmatch.ManagedRuleID,
			Title:          "A managed secret",
			Source:         secretmatch.SourceRememberedMatch,
			Reference:      refMarker,
			NonDisclosable: true,
		})
		return refMarker, nil
	})

	sink := &capturingRecipientProvider{}
	wrapped := &secretScreenedProvider{
		inner:       sink,
		destination: ScreenDestination{ID: "provider-main", Label: "Provider Main"},
		screen:      screen,
	}

	inbound := modelSecretRequest()
	inbound.Debug.SessionID = "sess-protect"
	inbound.Debug.RootSessionID = "sess-protect"

	_, err := wrapped.Complete(t.Context(), inbound)
	testutil.FailErr(t, "secretScreenedProvider.Complete", err)

	mu.Lock()
	gotAdopts := len(adoptCalls)
	gotEvidenceCalls := evidenceCalls
	mu.Unlock()

	if gotAdopts != 1 {
		t.Fatalf("adopter calls = %d, want 1", gotAdopts)
	}
	if gotEvidenceCalls < 2 {
		t.Fatalf("evidence calls = %d, want >= 2 (initial + post-adoption refresh)", gotEvidenceCalls)
	}
	if len(sink.received) != 1 {
		t.Fatalf("captured provider requests = %d, want 1", len(sink.received))
	}
	if bErr := verifyProviderRecipientBoundary(sink.received[0], []string{modelScreenGitHubToken}, refMarker); bErr != nil {
		t.Fatalf("outbound boundary leaked or missed reference: %v", bErr)
	}
	if !strings.Contains(inbound.Messages[0].Content, modelScreenGitHubToken) {
		t.Fatal("trackAndReplace mutated caller request")
	}
}

func TestModelSecretScreenTrackAndReplaceUsesReturnedReferenceWithoutEvidenceHook(t *testing.T) {
	t.Parallel()

	const refMarker = "{{paintedwolf-secret:dddddddd-dddd-4ddd-8ddd-dddddddddddd}}"
	m := modelScreenMatcher(t)

	screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.TrackAndReplace}, nil
	})
	screen.SetManagedSecretAdopter(func(_ context.Context, _ ManagedSecretAdoptRequest) (string, error) {
		return refMarker, nil
	})

	sink := &capturingRecipientProvider{}
	wrapped := &secretScreenedProvider{
		inner:       sink,
		destination: ScreenDestination{ID: "provider-main", Label: "Provider Main"},
		screen:      screen,
	}

	inbound := modelSecretRequest()
	inbound.Debug.SessionID = "sess-no-evidence"
	inbound.Debug.RootSessionID = "sess-no-evidence"

	_, err := wrapped.Complete(t.Context(), inbound)
	testutil.FailErr(t, "secretScreenedProvider.Complete", err)

	if len(sink.received) != 1 {
		t.Fatalf("captured provider requests = %d, want 1", len(sink.received))
	}
	if bErr := verifyProviderRecipientBoundary(sink.received[0], []string{modelScreenGitHubToken}, refMarker); bErr != nil {
		t.Fatalf("outbound boundary failed without evidence hook: %v", bErr)
	}
}

func TestModelSecretScreenTrackAndReplaceFailsClosedWithoutUsableReference(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		returnedRef      string
		failSecondLookup bool
	}{
		{name: "empty_reference", returnedRef: ""},
		{name: "whitespace_reference", returnedRef: "   "},
		{name: "evidence_refresh_error", returnedRef: "{{paintedwolf-secret:eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee}}", failSecondLookup: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := modelScreenMatcher(t)
			screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
				return secretmatch.Resolution{Decision: secretmatch.TrackAndReplace}, nil
			})
			screen.SetManagedSecretAdopter(func(_ context.Context, _ ManagedSecretAdoptRequest) (string, error) {
				return tc.returnedRef, nil
			})
			if tc.failSecondLookup {
				calls := 0
				screen.SetManagedSecretEvidence(func(context.Context, string, string) ([]secretmatch.Remembered, error) {
					calls++
					if calls == 1 {
						return nil, nil
					}
					return nil, errors.New("managed secret store unavailable")
				})
			}

			sink := &capturingRecipientProvider{}
			wrapped := &secretScreenedProvider{
				inner:       sink,
				destination: ScreenDestination{ID: "provider-main", Label: "Provider Main"},
				screen:      screen,
			}

			inbound := modelSecretRequest()
			inbound.Debug.SessionID = "sess-fc"
			inbound.Debug.RootSessionID = "sess-fc"

			_, err := wrapped.Complete(t.Context(), inbound)
			if err == nil {
				t.Fatal("expected fail-closed error, got nil")
			}
			var fault *ModelRequestSecretScreenFaultError
			if !errors.As(err, &fault) {
				t.Fatalf("err = %T (%v), want *ModelRequestSecretScreenFaultError", err, err)
			}
			if fault.Stage != secretmatch.FaultStageManagedSecretStore {
				t.Fatalf("fault.Stage = %q, want %q", fault.Stage, secretmatch.FaultStageManagedSecretStore)
			}
			if len(sink.received) != 0 {
				t.Fatalf("outbound provider received %d requests on fail-closed path, want 0", len(sink.received))
			}
		})
	}
}

func TestModelSecretScreenTrackAndReplaceMultiItemAndIdempotentRetry(t *testing.T) {
	t.Parallel()

	const secondSyntheticToken = "ghp_9xM2pL7vQ4nR8wK3zB6cT1yF5hJ0dS8uE2aN"
	refs := []string{
		"{{paintedwolf-secret:11111111-1111-4111-8111-111111111111}}",
		"{{paintedwolf-secret:22222222-2222-4222-8222-222222222222}}",
	}
	m := modelScreenMatcher(t)

	var (
		mu         sync.Mutex
		vaulted    []secretmatch.Remembered
		byOpID     = map[string]string{}
		askCalls   int
		adoptCalls int
	)

	// Mirror production durable evidence: HarvestSource reports durable
	// non-disclosable managed entries without reference tokens.
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		mu.Lock()
		defer mu.Unlock()
		out := make([]secretmatch.HarvestedValue, 0, len(vaulted))
		for _, v := range vaulted {
			out = append(out, secretmatch.HarvestedValue{
				Name:           v.Name,
				Container:      v.Origin,
				Secret:         v.Secret,
				RuleID:         v.RuleID,
				Title:          v.Title,
				Source:         v.Source,
				NonDisclosable: true,
			})
		}
		return out
	})

	screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		mu.Lock()
		askCalls++
		mu.Unlock()
		return secretmatch.Resolution{Decision: secretmatch.TrackAndReplace}, nil
	})
	screen.SetManagedSecretEvidence(func(_ context.Context, projectID, rootSessionID string) ([]secretmatch.Remembered, error) {
		mu.Lock()
		defer mu.Unlock()
		if projectID != "proj-1" || rootSessionID != "sess-multi" {
			return nil, errors.New("unexpected evidence scope")
		}
		out := make([]secretmatch.Remembered, len(vaulted))
		copy(out, vaulted)
		return out, nil
	})
	screen.SetManagedSecretAdopter(func(_ context.Context, req ManagedSecretAdoptRequest) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		adoptCalls++
		if existing, ok := byOpID[req.OperationID]; ok {
			return existing, nil
		}
		if len(vaulted) >= len(refs) {
			return "", errors.New("unexpected extra vault entry")
		}
		ref := refs[len(vaulted)]
		byOpID[req.OperationID] = ref
		vaulted = append(vaulted, secretmatch.Remembered{
			Secret:         req.Value,
			Name:           req.Name,
			Origin:         "managed secret",
			RuleID:         secretmatch.ManagedRuleID,
			Title:          "A managed secret",
			Source:         secretmatch.SourceRememberedMatch,
			Reference:      ref,
			NonDisclosable: true,
		})
		return ref, nil
	})

	sink := &capturingRecipientProvider{}
	wrapped := &secretScreenedProvider{
		inner:       sink,
		destination: ScreenDestination{ID: "provider-main", Label: "Provider Main"},
		screen:      screen,
	}

	inbound := modelSecretRequest()
	inbound.Debug.SessionID = "sess-multi"
	inbound.Debug.RootSessionID = "sess-multi"
	inbound.Messages[0].Content = "first=" + modelScreenGitHubToken + " second=" + secondSyntheticToken

	// First send: one checkpoint resolution protects both distinct credentials.
	_, err := wrapped.Complete(t.Context(), inbound)
	testutil.FailErr(t, "first send with two credentials", err)

	// Retry / second send in the same chat scope reuses both managed references idempotently.
	_, err = wrapped.Complete(t.Context(), inbound)
	testutil.FailErr(t, "retry send in same chat scope", err)

	mu.Lock()
	gotAsks := askCalls
	gotAdopts := adoptCalls
	gotVaulted := len(vaulted)
	mu.Unlock()

	if gotAsks != 1 {
		t.Fatalf("ask calls = %d, want 1 (single checkpoint for both items, none on retry)", gotAsks)
	}
	if gotAdopts != 2 || gotVaulted != 2 {
		t.Fatalf("adopter calls = %d, vaulted = %d; want 2 and 2 (idempotent without duplicates)", gotAdopts, gotVaulted)
	}
	if len(sink.received) != 2 {
		t.Fatalf("captured provider requests = %d, want 2", len(sink.received))
	}
	for i, delivered := range sink.received {
		for _, ref := range refs {
			if bErr := verifyProviderRecipientBoundary(delivered, []string{modelScreenGitHubToken, secondSyntheticToken}, ref); bErr != nil {
				t.Fatalf("request %d failed recipient-boundary check for %s: %v", i, ref, bErr)
			}
		}
	}
	if !strings.Contains(inbound.Messages[0].Content, modelScreenGitHubToken) || !strings.Contains(inbound.Messages[0].Content, secondSyntheticToken) {
		t.Fatal("trackAndReplace mutated caller request")
	}
}
