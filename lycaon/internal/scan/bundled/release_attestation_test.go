package bundled

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const attestationTestTag = "v1.30.0+paintedwolf.33"

func workflowProof(t *testing.T) verifiedReleaseAttestation {
	t.Helper()
	var proof verifiedReleaseAttestation
	proof.Attestation.Bundle = json.RawMessage(`{"mediaType":"test-only-mocked-verifier-bundle"}`)
	proof.Result.Timestamps = []json.RawMessage{json.RawMessage(`{"timestamp":"2026-09-09T00:00:00Z"}`)}
	proof.Result.Statement.PredicateType = "https://slsa.dev/provenance/v1"
	proof.Result.Statement.Subject = append(proof.Result.Statement.Subject, struct {
		Digest map[string]string `json:"digest"`
	}{Digest: map[string]string{"sha256": strings.Repeat("d", 64)}})
	proof.Result.Signature.Certificate = releaseCertificate{
		Issuer: releaseIssuer, SAN: "https://github.com/" + releaseWorkflow + "@refs/tags/" + attestationTestTag,
		SignerURI:    "https://github.com/" + releaseWorkflow + "@refs/tags/" + attestationTestTag,
		SignerDigest: strings.Repeat("a", 40), Runner: "github-hosted", Repository: "https://github.com/" + releaseRepository,
		Commit: strings.Repeat("a", 40), Ref: "refs/tags/" + attestationTestTag, RepositoryID: releaseRepositoryID, OwnerID: releaseOwnerID,
	}
	return proof
}

func TestAttestationRequiresCertifiedPublisherIdentity(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*verifiedReleaseAttestation){
		"qualified":            func(*verifiedReleaseAttestation) {},
		"repository recreated": func(p *verifiedReleaseAttestation) { p.Result.Signature.Certificate.RepositoryID = "1" },
		"owner changed":        func(p *verifiedReleaseAttestation) { p.Result.Signature.Certificate.OwnerID = "1" },
		"source commit":        func(p *verifiedReleaseAttestation) { p.Result.Signature.Certificate.Commit = strings.Repeat("b", 40) },
		"workflow commit": func(p *verifiedReleaseAttestation) {
			p.Result.Signature.Certificate.SignerDigest = strings.Repeat("b", 40)
		},
		"source ref":     func(p *verifiedReleaseAttestation) { p.Result.Signature.Certificate.Ref = "refs/heads/main" },
		"self hosted":    func(p *verifiedReleaseAttestation) { p.Result.Signature.Certificate.Runner = "self-hosted" },
		"missing runner": func(p *verifiedReleaseAttestation) { p.Result.Signature.Certificate.Runner = "" },
		"workflow SAN":   func(p *verifiedReleaseAttestation) { p.Result.Signature.Certificate.SAN = "https://github.com/other" },
		"workflow URI": func(p *verifiedReleaseAttestation) {
			p.Result.Signature.Certificate.SignerURI = "https://github.com/other"
		},
		"issuer": func(p *verifiedReleaseAttestation) { p.Result.Signature.Certificate.Issuer = "https://issuer.invalid" },
		"repository": func(p *verifiedReleaseAttestation) {
			p.Result.Signature.Certificate.Repository = "https://github.com/other"
		},
		"subject": func(p *verifiedReleaseAttestation) {
			p.Result.Statement.Subject[0].Digest["sha256"] = strings.Repeat("e", 64)
		},
		"predicate type":         func(p *verifiedReleaseAttestation) { p.Result.Statement.PredicateType = "https://example.invalid" },
		"no witnessed timestamp": func(p *verifiedReleaseAttestation) { p.Result.Timestamps = nil },
		"no bundle":              func(p *verifiedReleaseAttestation) { p.Attestation.Bundle = nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			proof := workflowProof(t)
			mutate(&proof)
			raw, err := json.Marshal([]verifiedReleaseAttestation{proof})
			testutil.FailErr(t, "encode verifier output", err)
			err = verifyWorkflowClaims(raw, attestationTestTag, strings.Repeat("a", 40), strings.Repeat("d", 64))
			if (err == nil) != (name == "qualified") {
				t.Fatalf("publisher admission error=%v", err)
			}
		})
	}
}

func TestAttestationRejectsReceiptAndPredicateAsAuthority(t *testing.T) {
	t.Parallel()
	proof := workflowProof(t)
	raw, err := json.Marshal(proof)
	testutil.FailErr(t, "encode fake predicate", err)
	var value map[string]any
	testutil.FailErr(t, "decode mocked output", json.Unmarshal(raw, &value))
	result := value["verificationResult"].(map[string]any)
	result["statement"].(map[string]any)["predicate"] = result["signature"]
	delete(result, "signature")
	for _, candidate := range []any{value, []any{value}, map[string]any{"verified": true}, []any{}} {
		raw, err = json.Marshal(candidate)
		testutil.FailErr(t, "encode forged authority", err)
		if verifyWorkflowClaims(raw, attestationTestTag, strings.Repeat("a", 40), strings.Repeat("d", 64)) == nil {
			t.Fatal("uncertified identity admitted")
		}
	}
}

func TestVerifierOutputIsBoundedAndUnambiguous(t *testing.T) {
	t.Parallel()
	output := &releaseCommandOutput{limit: 8}
	_, err := io.Copy(output, strings.NewReader(strings.Repeat("x", 9)))
	if err == nil || output.buffer.Len() > 8 {
		t.Fatal("output limit bypassed by io.Copy")
	}
	var value any
	for _, raw := range [][]byte{[]byte(`{"verificationResult":{},"verificationResult":{}}`), []byte(`{} {}`), bytes.Repeat([]byte(" "), maxVerifierOutputBytes+1)} {
		if decodeVerifierJSON(raw, &value) == nil {
			t.Fatal("ambiguous or oversized verifier output accepted")
		}
	}
}

func TestWorkflowVerifierFailureCannotBecomeReceiptProof(t *testing.T) {
	t.Parallel()
	want := errors.New("cryptographic verification failed")
	run := func(context.Context, ...string) ([]byte, error) { return []byte(`{"verified":true}`), want }
	_, err := authenticateReleaseFile(t.Context(), run, "/artifact", strings.Repeat("d", 64), attestationTestTag, strings.Repeat("a", 40))
	if !errors.Is(err, want) {
		t.Fatalf("verifier failure lost: %v", err)
	}
}

func TestSelectionRequiresIndependentIdentityAndOnlineState(t *testing.T) {
	t.Parallel()
	valid := AttestedReleaseSelection{Descriptor: "/release.json", Destination: "/pin.yaml", Tag: attestationTestTag, ExpectedCommit: strings.Repeat("a", 40), Fetch: ReleaseFetchOptions{CacheRoot: "/cache"}}
	for name, mutate := range map[string]func(*AttestedReleaseSelection){
		"no commit":       func(s *AttestedReleaseSelection) { s.ExpectedCommit = "" },
		"short commit":    func(s *AttestedReleaseSelection) { s.ExpectedCommit = "abc" },
		"ref injection":   func(s *AttestedReleaseSelection) { s.Tag = "v1/../../other" },
		"offline receipt": func(s *AttestedReleaseSelection) { s.Fetch.Offline = true },
		"different repository": func(s *AttestedReleaseSelection) {
			s.Descriptor = "https://github.com/other/repo/releases/download/" + s.Tag + "/release.json"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			selection := valid
			mutate(&selection)
			if selection.validate() == nil {
				t.Fatal("unbounded selection accepted")
			}
		})
	}
}

func TestReleaseStateEvidenceIgnoresDownloadCounters(t *testing.T) {
	record := func(downloads int, reactions string) []byte {
		return []byte(`{"id":401327992,"tag_name":"` + attestationTestTag + `","immutable":true,` + reactions +
			`"assets":[{"id":604078267,"name":"release.json","digest":"sha256:` + strings.Repeat("a", 64) +
			`","download_count":` + strings.Repeat("9", downloads) + `0}]}`)
	}
	first, err := stableReleaseState(record(0, ""))
	testutil.FailErr(t, "normalize first release state", err)
	second, err := stableReleaseState(record(4, `"reactions":{"total_count":3},"mentions_count":1,`))
	testutil.FailErr(t, "normalize second release state", err)
	if !bytes.Equal(first, second) {
		t.Fatalf("release state evidence changed with counters:\n%s\n%s", first, second)
	}
	for _, kept := range []string{`"id":401327992`, `"id":604078267`, `"digest":"sha256:`, `"immutable":true`} {
		if !bytes.Contains(first, []byte(kept)) {
			t.Fatalf("release state evidence dropped %s: %s", kept, first)
		}
	}
	if bytes.Contains(first, []byte("download_count")) {
		t.Fatalf("release state evidence kept download_count: %s", first)
	}
}
