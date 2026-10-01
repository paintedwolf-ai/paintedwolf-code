package bundled

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	releaseRepository   = "paintedwolf-ai/paintedwolf-opengrep"
	releaseRepositoryID = "1362758908"
	releaseOwnerID      = "289211278"
	releaseWorkflow     = releaseRepository + "/.github/workflows/native-release.yml"
	releaseIssuer       = "https://token.actions.githubusercontent.com"
)

// These fields contain certified OIDC claims from the verifier output.
type releaseCertificate struct {
	Issuer       string `json:"issuer"`
	SAN          string `json:"subjectAlternativeName"`
	SignerURI    string `json:"buildSignerURI"`
	SignerDigest string `json:"buildSignerDigest"`
	Runner       string `json:"runnerEnvironment"`
	Repository   string `json:"sourceRepositoryURI"`
	Commit       string `json:"sourceRepositoryDigest"`
	Ref          string `json:"sourceRepositoryRef"`
	RepositoryID string `json:"sourceRepositoryIdentifier"`
	OwnerID      string `json:"sourceRepositoryOwnerIdentifier"`
}

type verifiedReleaseAttestation struct {
	Attestation struct {
		Bundle json.RawMessage `json:"bundle"`
	} `json:"attestation"`
	Result struct {
		Signature struct {
			Certificate releaseCertificate `json:"certificate"`
		} `json:"signature"`
		Timestamps []json.RawMessage `json:"verifiedTimestamps"`
		Statement  struct {
			PredicateType string `json:"predicateType"`
			Subject       []struct {
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
		} `json:"statement"`
	} `json:"verificationResult"`
}

func decodeVerifierJSON(raw []byte, value any) error {
	if len(raw) == 0 || len(raw) > maxVerifierOutputBytes {
		return fmt.Errorf("invalid verifier output size")
	}
	if err := rejectVerifierDuplicateFields(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return fmt.Errorf("ambiguous verifier output: %w", err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("invalid verifier output: %w", err)
	}
	return nil
}

func rejectVerifierDuplicateFields(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return fmt.Errorf("verifier JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, nested := token.(json.Delim)
	if !nested {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return fmt.Errorf("invalid verifier JSON delimiter")
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, valid := key.(string)
			name = strings.ToLower(name)
			if !valid || seen[name] {
				return fmt.Errorf("duplicate verifier JSON field")
			}
			seen[name] = true
		}
		if err := rejectVerifierDuplicateFields(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func (a verifiedReleaseAttestation) bindsSubject(digest string) bool {
	if len(a.Result.Timestamps) == 0 || len(a.Attestation.Bundle) == 0 || bytes.Equal(a.Attestation.Bundle, []byte("null")) {
		return false
	}
	for _, subject := range a.Result.Statement.Subject {
		if subject.Digest["sha256"] == digest {
			return true
		}
	}
	return false
}

func verifyWorkflowClaims(raw []byte, tag, commit, digest string) error {
	var attestations []verifiedReleaseAttestation
	if err := decodeVerifierJSON(raw, &attestations); err != nil {
		return err
	}
	want := releaseCertificate{
		Issuer: releaseIssuer, SAN: "https://github.com/" + releaseWorkflow + "@refs/tags/" + tag,
		SignerURI:    "https://github.com/" + releaseWorkflow + "@refs/tags/" + tag,
		SignerDigest: commit, Runner: "github-hosted", Repository: "https://github.com/" + releaseRepository,
		Commit: commit, Ref: "refs/tags/" + tag, RepositoryID: releaseRepositoryID, OwnerID: releaseOwnerID,
	}
	for _, attestation := range attestations {
		if attestation.Result.Signature.Certificate == want && attestation.bindsSubject(digest) &&
			attestation.Result.Statement.PredicateType == "https://slsa.dev/provenance/v1" {
			return nil
		}
	}
	return fmt.Errorf("no verified attestation matches the approved repository, workflow, commit, tag, runner and artifact")
}

func verifyReleaseClaims(raw []byte, digest string) error {
	var attestation verifiedReleaseAttestation
	if err := decodeVerifierJSON(raw, &attestation); err != nil {
		return err
	}
	if attestation.Result.Signature.Certificate.SAN != "https://dotcom.releases.github.com" || !attestation.bindsSubject(digest) {
		return fmt.Errorf("immutable release verification does not bind the artifact")
	}
	return nil
}
