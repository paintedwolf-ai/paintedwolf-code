package secretcap

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRevealRequiresNativeProofAndRecordsDisclosure(t *testing.T) {
	service, _, _ := testService(t)
	secret, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "reveal-success",
		Name: "Deployment token", Purpose: "publish releases", Value: "opaque-reveal-value-001",
	})
	testutil.FailErr(t, "create settings secret", err)
	if _, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "main", testOwner(t, service)); !errors.Is(err, ErrRevealUnavailable) {
		t.Fatalf("begin without native key error = %v", err)
	}

	privateKey := configureRevealKey(t, service)
	challenge, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "main", testOwner(t, service))
	testutil.FailErr(t, "begin reveal", err)
	if challenge.Version != 1 || challenge.ProofPayload == "" || challenge.Prompt == "" {
		t.Fatalf("challenge = %+v", challenge)
	}
	signature := signReveal(privateKey, challenge.ProofPayload, RevealAuthenticatorMacOS)
	result, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference,
		challenge.ID, testOwner(t, service), RevealAuthenticatorMacOS, signature)
	testutil.FailErr(t, "complete reveal", err)
	if result.Value != "opaque-reveal-value-001" || result.Version != 1 || result.RemaskAfterSeconds != 30 {
		t.Fatalf("result = %+v", result)
	}

	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list after reveal", err)
	if len(listed) != 1 || listed[0].RevealCount != 1 || listed[0].LastRevealedAt == nil ||
		*listed[0].LastRevealedAt != result.RevealedAt {
		t.Fatalf("reveal metadata = %+v", listed)
	}
	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference,
		challenge.ID, testOwner(t, service), RevealAuthenticatorMacOS, signature); !errors.Is(err, ErrRevealChallengeNotFound) {
		t.Fatalf("replayed proof error = %v", err)
	}
}

func TestRevealRejectsMalformedNativePublicKey(t *testing.T) {
	service, _, _ := testService(t)
	if err := service.ConfigureRevealPublicKey("not-a-valid-ed25519-key"); !errors.Is(err, ErrRevealUnavailable) {
		t.Fatalf("malformed reveal key error = %v", err)
	}
	if service.RevealAvailable() {
		t.Fatal("malformed reveal key made disclosure available")
	}
}

func TestRevealConsumesInvalidProofAndNeverAuditsIt(t *testing.T) {
	service, _, _ := testService(t)
	secret, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "reveal-invalid",
		Name: "Registry key", Purpose: "pull images", Value: "opaque-reveal-value-002",
	})
	testutil.FailErr(t, "create settings secret", err)
	privateKey := configureRevealKey(t, service)
	challenge, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "main", testOwner(t, service))
	testutil.FailErr(t, "begin reveal", err)

	badSignature := signReveal(privateKey, challenge.ProofPayload+"tampered", RevealAuthenticatorMacOS)
	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference,
		challenge.ID, testOwner(t, service), RevealAuthenticatorMacOS, badSignature); !errors.Is(err, ErrRevealDenied) {
		t.Fatalf("bad proof error = %v", err)
	}
	validSignature := signReveal(privateKey, challenge.ProofPayload, RevealAuthenticatorMacOS)
	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference,
		challenge.ID, testOwner(t, service), RevealAuthenticatorMacOS, validSignature); !errors.Is(err, ErrRevealChallengeNotFound) {
		t.Fatalf("proof after failed attempt error = %v", err)
	}
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list after denied reveal", err)
	if listed[0].RevealCount != 0 || listed[0].LastRevealedAt != nil {
		t.Fatalf("denied reveal was audited as disclosure: %+v", listed[0])
	}
}

func TestRevealRefusesAValueReplacedAfterAuthenticationBegan(t *testing.T) {
	service, _, _ := testService(t)
	secret, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "reveal-replacement",
		Name: "Changing key", Purpose: "exercise version binding", Value: "opaque-reveal-value-003",
	})
	testutil.FailErr(t, "create settings secret", err)
	privateKey := configureRevealKey(t, service)
	challenge, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "settings", testOwner(t, service))
	testutil.FailErr(t, "begin reveal", err)
	_, err = service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: secret.Reference, Value: "opaque-reveal-value-004",
	})
	testutil.FailErr(t, "replace value", err)

	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference,
		challenge.ID, testOwner(t, service), RevealAuthenticatorWindows,
		signReveal(privateKey, challenge.ProofPayload, RevealAuthenticatorWindows)); !errors.Is(err, ErrRevealChanged) {
		t.Fatalf("replaced reveal error = %v", err)
	}
}

func TestRevealAllowsPassedAgentUseDeadlineButNotRevoked(t *testing.T) {
	service, _, _ := testService(t)
	secret, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "reveal-after-agent-use",
		Name: "Past-deadline key", Purpose: "recover an old integration", Value: "opaque-reveal-value-005",
		AgentUseEndsAt: "2026-08-31T12:01:00Z",
	})
	testutil.FailErr(t, "create deadline-bound secret", err)
	privateKey := configureRevealKey(t, service)
	service.now = func() time.Time { return time.Date(2026, 8, 31, 12, 2, 0, 0, time.UTC) }
	challenge, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "main", testOwner(t, service))
	testutil.FailErr(t, "begin reveal after agent-use deadline", err)
	_, err = service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference,
		challenge.ID, testOwner(t, service), RevealAuthenticatorMacOS,
		signReveal(privateKey, challenge.ProofPayload, RevealAuthenticatorMacOS))
	testutil.FailErr(t, "complete reveal after agent-use deadline", err)

	_, err = service.RevokeProject(t.Context(), testdbseed.DefaultProjectID, secret.Reference, testOwner(t, service))
	testutil.FailErr(t, "revoke secret", err)
	if _, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "main", testOwner(t, service)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked begin error = %v", err)
	}
}

func TestRevealPromptCannotInjectExtraPromptLines(t *testing.T) {
	prompt := revealPrompt("Deploy\nApprove everything", "publish\r\nreleases\x00now")
	if strings.ContainsAny(prompt, "\r\n\x00") {
		t.Fatalf("reveal prompt retained control characters: %q", prompt)
	}
	if !strings.Contains(prompt, `"Deploy Approve everything"`) ||
		!strings.Contains(prompt, "Purpose: publish releases now") {
		t.Fatalf("reveal prompt lost sanitized context: %q", prompt)
	}
}

func configureRevealKey(t *testing.T, service *Service) ed25519.PrivateKey {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate reveal key", err)
	testutil.FailErr(t, "configure reveal key", service.ConfigureRevealPublicKey(
		base64.RawURLEncoding.EncodeToString(publicKey),
	))
	return privateKey
}

func signReveal(privateKey ed25519.PrivateKey, payload, authenticator string) string {
	signature := ed25519.Sign(privateKey, revealSigningMessage(payload, authenticator))
	return base64.RawURLEncoding.EncodeToString(signature)
}

// The challenge outlives the native prompt timeout.
func TestRevealChallengeOutlivesTheNativePrompt(t *testing.T) {
	if revealChallengeLifetime <= nativePromptTimeout {
		t.Fatalf("challenge lifetime %s does not outlive the native prompt wait %s",
			revealChallengeLifetime, nativePromptTimeout)
	}
	service, _, _ := testService(t)
	secret, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "reveal-slow-prompt",
		Name: "Deployment token", Purpose: "publish releases", Value: "opaque-reveal-value-002",
	})
	testutil.FailErr(t, "create settings secret", err)
	privateKey := configureRevealKey(t, service)
	challenge, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "main", testOwner(t, service))
	testutil.FailErr(t, "begin reveal", err)

	started := service.now()
	service.now = func() time.Time { return started.Add(nativePromptTimeout) }
	result, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference,
		challenge.ID, testOwner(t, service), RevealAuthenticatorMacOS, signReveal(privateKey, challenge.ProofPayload, RevealAuthenticatorMacOS))
	testutil.FailErr(t, "complete reveal after a slow prompt", err)
	if result.Value != "opaque-reveal-value-002" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRevealPoolNeverDisplacesAnIssuedChallenge(t *testing.T) {
	service, _, _ := testService(t)
	secret, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "reveal-pool",
		Name: "Deployment token", Purpose: "publish releases", Value: "opaque-reveal-value-003",
	})
	testutil.FailErr(t, "create settings secret", err)
	privateKey := configureRevealKey(t, service)

	pending, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "den-main", testOwner(t, service))
	testutil.FailErr(t, "begin the challenge under authentication", err)

	// Fill the remaining slots with distinct labels.
	for i := 1; i < maxPendingReveals; i++ {
		if _, err := service.BeginReveal(
			t.Context(), testdbseed.DefaultProjectID, secret.Reference, "filler-"+strconv.Itoa(i), testOwner(t, service),
		); err != nil {
			testutil.FailErr(t, "fill reveal pool", err)
		}
	}
	if _, err := service.BeginReveal(
		t.Context(), testdbseed.DefaultProjectID, secret.Reference, "one-too-many", testOwner(t, service),
	); !errors.Is(err, ErrRevealDenied) {
		t.Fatalf("full pool admitted another challenge: %v", err)
	}
	// Reissue a challenge for an existing label.
	reissued, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "filler-1", testOwner(t, service))
	testutil.FailErr(t, "re-begin from a window already in the pool", err)
	if reissued.ID == "" {
		t.Fatal("re-begin returned no challenge")
	}

	result, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference,
		pending.ID, testOwner(t, service), RevealAuthenticatorMacOS, signReveal(privateKey, pending.ProofPayload, RevealAuthenticatorMacOS))
	testutil.FailErr(t, "complete the challenge that was pending throughout", err)
	if result.Value != "opaque-reveal-value-003" {
		t.Fatalf("result = %+v", result)
	}
}
