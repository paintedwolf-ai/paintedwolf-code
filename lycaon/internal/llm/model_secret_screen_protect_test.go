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
