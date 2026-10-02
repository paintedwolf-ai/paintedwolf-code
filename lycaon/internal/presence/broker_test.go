package presence

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

type testSubject struct {
	Item string `json:"item"`
}

func configuredBroker(t *testing.T) (*Broker, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate key", err)
	broker := NewBroker()
	testutil.FailErr(t, "configure key", broker.Configure(base64.RawURLEncoding.EncodeToString(publicKey)))
	return broker, privateKey
}

func sign(privateKey ed25519.PrivateKey, challenge Challenge, authenticator string) Proof {
	signature := ed25519.Sign(privateKey, SigningMessage(challenge.ProofPayload, authenticator))
	return Proof{ChallengeID: challenge.ID, Authenticator: authenticator, Signature: base64.RawURLEncoding.EncodeToString(signature)}
}

func TestBrokerVerifiesTheSignedSubjectForThePersonWhoBegan(t *testing.T) {
	broker, privateKey := configuredBroker(t)
	challenge, err := broker.Begin(Claim{Purpose: PurposeUnlock, PersonID: "owner", WindowLabel: "main", Key: "one", Subject: testSubject{Item: "card"}})
	testutil.FailErr(t, "begin", err)
	verified, err := broker.Complete(sign(privateKey, challenge, AuthenticatorMacOS), PurposeUnlock, "owner")
	testutil.FailErr(t, "complete", err)
	var subject testSubject
	testutil.FailErr(t, "decode subject", json.Unmarshal(verified.Subject, &subject))
	if subject.Item != "card" || verified.PersonID != "owner" || verified.WindowLabel != "main" ||
		verified.Authenticator != AuthenticatorMacOS || verified.ChallengeID != challenge.ID {
		t.Fatalf("verified = %+v subject = %+v", verified, subject)
	}
	if _, err := broker.Complete(sign(privateKey, challenge, AuthenticatorMacOS), PurposeUnlock, "owner"); !errors.Is(err, ErrChallengeNotFound) {
		t.Fatalf("replayed proof error = %v", err)
	}
}

// A proof answers the purpose it was issued for and nothing else.
func TestBrokerRefusesAProofForAnotherPurposeOrPerson(t *testing.T) {
	broker, privateKey := configuredBroker(t)
	reveal, err := broker.Begin(Claim{Purpose: PurposeReveal, PersonID: "owner", WindowLabel: "main", Key: "secret"})
	testutil.FailErr(t, "begin reveal", err)
	if _, err := broker.Complete(sign(privateKey, reveal, AuthenticatorMacOS), PurposeUnlock, "owner"); !errors.Is(err, ErrDenied) {
		t.Fatalf("reveal proof answered a release: %v", err)
	}
	release, err := broker.Begin(Claim{Purpose: PurposeUnlock, PersonID: "owner", WindowLabel: "main", Key: "card"})
	testutil.FailErr(t, "begin release", err)
	if _, err := broker.Complete(sign(privateKey, release, AuthenticatorMacOS), PurposeUnlock, "someone-else"); !errors.Is(err, ErrDenied) {
		t.Fatalf("another person completed a release: %v", err)
	}
}

func TestBrokerRefusesAForgedSignature(t *testing.T) {
	broker, _ := configuredBroker(t)
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate other key", err)
	challenge, err := broker.Begin(Claim{Purpose: PurposeUnlock, PersonID: "owner", WindowLabel: "main", Key: "card"})
	testutil.FailErr(t, "begin", err)
	if _, err := broker.Complete(sign(otherKey, challenge, AuthenticatorMacOS), PurposeUnlock, "owner"); !errors.Is(err, ErrDenied) {
		t.Fatalf("forged signature error = %v", err)
	}
}

func TestBrokerWithoutAKeyIsUnavailable(t *testing.T) {
	broker := NewBroker()
	if broker.Available() {
		t.Fatal("broker without a key reported available")
	}
	if _, err := broker.Begin(Claim{Purpose: PurposeReveal, PersonID: "owner", WindowLabel: "main"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("begin without key error = %v", err)
	}
	if err := broker.Configure("not-a-valid-ed25519-key"); !errors.Is(err, ErrUnavailable) || broker.Available() {
		t.Fatalf("malformed key error = %v, available = %v", err, broker.Available())
	}
}

// The challenge outlives the native prompt timeout.
func TestBrokerChallengeOutlivesTheNativePrompt(t *testing.T) {
	if challengeLifetime <= PromptTimeout {
		t.Fatalf("challenge lifetime %s does not outlive the prompt %s", challengeLifetime, PromptTimeout)
	}
	broker, privateKey := configuredBroker(t)
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	broker.SetClock(func() time.Time { return started })
	challenge, err := broker.Begin(Claim{Purpose: PurposeReveal, PersonID: "owner", WindowLabel: "main", Key: "secret"})
	testutil.FailErr(t, "begin", err)
	broker.SetClock(func() time.Time { return started.Add(PromptTimeout) })
	_, err = broker.Complete(sign(privateKey, challenge, AuthenticatorMacOS), PurposeReveal, "owner")
	testutil.FailErr(t, "complete after a slow prompt", err)
}

func TestBrokerPoolNeverDisplacesAnIssuedChallenge(t *testing.T) {
	broker, privateKey := configuredBroker(t)
	pending, err := broker.Begin(Claim{Purpose: PurposeReveal, PersonID: "owner", WindowLabel: "den-main", Key: "secret"})
	testutil.FailErr(t, "begin the challenge under verification", err)
	for i := 1; i < maxPending; i++ {
		_, err := broker.Begin(Claim{Purpose: PurposeReveal, PersonID: "owner", WindowLabel: "filler-" + strconv.Itoa(i), Key: "secret"})
		testutil.FailErr(t, "fill pool", err)
	}
	if _, err := broker.Begin(Claim{Purpose: PurposeReveal, PersonID: "owner", WindowLabel: "one-too-many", Key: "secret"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("full pool admitted another challenge: %v", err)
	}
	if _, err := broker.Begin(Claim{Purpose: PurposeReveal, PersonID: "owner", WindowLabel: "filler-1", Key: "secret"}); err != nil {
		testutil.FailErr(t, "re-begin from a window already in the pool", err)
	}
	_, err = broker.Complete(sign(privateKey, pending, AuthenticatorMacOS), PurposeReveal, "owner")
	testutil.FailErr(t, "complete the challenge that was pending throughout", err)
}

func TestTrustedKeyNeedsAVerifiedLauncherOnlyForAnUnattendedVault(t *testing.T) {
	refused := func() error { return ErrLauncherUnverified }
	if key, err := TrustedKey("key", true, refused); key != "" || !errors.Is(err, ErrLauncherUnverified) {
		t.Fatalf("unattended vault accepted an unverified launcher: %q %v", key, err)
	}
	if key, err := TrustedKey("key", true, func() error { return nil }); key != "key" || err != nil {
		t.Fatalf("verified launcher refused: %q %v", key, err)
	}
	if key, err := TrustedKey("key", false, refused); key != "key" || err != nil {
		t.Fatalf("password vault consulted the launcher: %q %v", key, err)
	}
}
