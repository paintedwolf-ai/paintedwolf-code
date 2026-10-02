package secretcap

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/presence"
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
	if _, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "main", testOwner(t, service)); !errors.Is(err, presence.ErrUnavailable) {
		t.Fatalf("begin without native key error = %v", err)
	}

	privateKey := configureRevealKey(t, service)
	challenge, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "main", testOwner(t, service))
	testutil.FailErr(t, "begin reveal", err)
	if challenge.Version != 1 || challenge.ProofPayload == "" || challenge.Prompt == "" {
		t.Fatalf("challenge = %+v", challenge)
	}
	proof := signedProof(privateKey, challenge, presence.AuthenticatorMacOS)
	result, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, testOwner(t, service), proof)
	testutil.FailErr(t, "complete reveal", err)
	if result.Value != "opaque-reveal-value-001" || result.Version != 1 || result.RemaskAfterSeconds != 30 {
		t.Fatalf("result = %+v", result)
	}

	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list after reveal", err)
	if len(listed) != 1 || listed[0].RevealCount != 1 || listed[0].LastRevealedAt == nil ||
		*listed[0].LastRevealedAt != result.RevealedAt || listed[0].ReleaseCount != 0 {
		t.Fatalf("reveal metadata = %+v", listed)
	}
	attestations, err := service.Attestations(t.Context(), testdbseed.DefaultProjectID, secret.Reference, 0)
	testutil.FailErr(t, "list attestations", err)
	if len(attestations) != 1 || attestations[0].Purpose != string(presence.PurposeReveal) ||
		attestations[0].AttestationID != challenge.ID || attestations[0].Authenticator != presence.AuthenticatorMacOS {
		t.Fatalf("reveal attestation = %+v", attestations)
	}
	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, testOwner(t, service), proof); !errors.Is(err, presence.ErrChallengeNotFound) {
		t.Fatalf("replayed proof error = %v", err)
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

	tampered := challenge
	tampered.ProofPayload += "tampered"
	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, testOwner(t, service),
		signedProof(privateKey, tampered, presence.AuthenticatorMacOS)); !errors.Is(err, presence.ErrDenied) {
		t.Fatalf("bad proof error = %v", err)
	}
	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, testOwner(t, service),
		signedProof(privateKey, challenge, presence.AuthenticatorMacOS)); !errors.Is(err, presence.ErrChallengeNotFound) {
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

	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, testOwner(t, service),
		signedProof(privateKey, challenge, presence.AuthenticatorWindows)); !errors.Is(err, ErrValueChanged) {
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
	_, err = service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, testOwner(t, service),
		signedProof(privateKey, challenge, presence.AuthenticatorMacOS))
	testutil.FailErr(t, "complete reveal after agent-use deadline", err)

	_, err = service.RevokeProject(t.Context(), testdbseed.DefaultProjectID, secret.Reference, testOwner(t, service))
	testutil.FailErr(t, "revoke secret", err)
	if _, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, secret.Reference, "main", testOwner(t, service)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked begin error = %v", err)
	}
}

// A reveal challenge names the value it reveals; its proof cannot reveal another.
func TestRevealProofCannotRevealAnotherValue(t *testing.T) {
	service, _, _ := testService(t)
	first, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "reveal-first",
		Name: "First", Purpose: "first value", Value: "opaque-reveal-first-001",
	})
	testutil.FailErr(t, "create first secret", err)
	second, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "reveal-second",
		Name: "Second", Purpose: "second value", Value: "opaque-reveal-second-001",
	})
	testutil.FailErr(t, "create second secret", err)
	privateKey := configureRevealKey(t, service)
	challenge, err := service.BeginReveal(t.Context(), testdbseed.DefaultProjectID, first.Reference, "main", testOwner(t, service))
	testutil.FailErr(t, "begin first reveal", err)
	if _, err := service.CompleteReveal(t.Context(), testdbseed.DefaultProjectID, second.Reference, testOwner(t, service),
		signedProof(privateKey, challenge, presence.AuthenticatorMacOS)); !errors.Is(err, presence.ErrDenied) {
		t.Fatalf("cross-value reveal error = %v", err)
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
	testutil.FailErr(t, "generate presence key", err)
	broker := presence.NewBroker()
	testutil.FailErr(t, "configure presence key", broker.Configure(base64.RawURLEncoding.EncodeToString(publicKey)))
	service.SetPresence(broker)
	return privateKey
}

func signedProof(privateKey ed25519.PrivateKey, challenge RevealChallenge, authenticator string) presence.Proof {
	signature := ed25519.Sign(privateKey, presence.SigningMessage(challenge.ProofPayload, authenticator))
	return presence.Proof{
		ChallengeID: challenge.ID, Authenticator: authenticator,
		Signature: base64.RawURLEncoding.EncodeToString(signature),
	}
}
