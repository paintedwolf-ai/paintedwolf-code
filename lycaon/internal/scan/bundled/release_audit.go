package bundled

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"

	"github.com/lycaon/lycaon/internal/fseffect"
)

const maxReleaseEvidenceBytes = 32 << 20

type releaseAudit struct {
	SchemaVersion int                        `json:"schema_version"`
	Mode          string                     `json:"mode"`
	Engine        OpenGrepManifest           `json:"engine"`
	Identity      *BuildIdentity             `json:"build_identity"`
	Selection     *ReleaseSelectionReference `json:"selection,omitempty"`
	Evidence      []byte                     `json:"evidence_bytes,omitempty"`
}

// WriteReleaseAudit checks packaged bytes and links offline evidence through its pinned digest.
func WriteReleaseAudit(ctx context.Context, m *Manifest, packagedDirectory, evidenceRoot, destination string) error {
	if err := ValidateManifest(m); err != nil {
		return err
	}
	if err := m.verifyReleaseSelection(ctx); err != nil {
		return err
	}
	if packagedDirectory == "" {
		return fmt.Errorf("release audit requires the packaged engine directory")
	}
	if err := verifyArtifactDirectory(m, packagedDirectory); err != nil {
		return fmt.Errorf("packaged engine differs from selected release: %w", err)
	}
	audit := releaseAudit{SchemaVersion: 1, Mode: "reviewed-pin-only", Engine: m.OpenGrep, Identity: m.identity, Selection: m.OpenGrepSelection}
	if m.OpenGrepSelection != nil {
		raw, err := readSelectionEvidence(m, evidenceRoot)
		if err != nil {
			return err
		}
		audit.Mode = "authenticated-selection"
		audit.Evidence = raw
	}
	raw, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: filepath.Dir(absolute), Rel: filepath.Base(absolute)}, Source: bytes.NewReader(append(raw, '\n')), Mode: 0o644})
	return err
}

func readSelectionEvidence(m *Manifest, root string) ([]byte, error) {
	if root == "" {
		return nil, fmt.Errorf("authenticated selection audit requires the committed evidence root")
	}
	reference := m.OpenGrepSelection
	file, err := openReleaseFile(filepath.Join(root, reference.evidenceFile()))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxReleaseEvidenceBytes+1))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if len(raw) > maxReleaseEvidenceBytes || hex.EncodeToString(digest[:]) != reference.EvidenceSHA256 {
		return nil, fmt.Errorf("selection evidence differs from committed digest")
	}
	if err := validateSelectionEvidence(m, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func validateSelectionEvidence(m *Manifest, raw []byte) error {
	if err := rejectVerifierDuplicateFields(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return err
	}
	var evidence releaseSelectionEvidence
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("selection evidence requires one object")
	}
	reference := m.OpenGrepSelection
	descriptorDigest := sha256.Sum256(evidence.Descriptor)
	if evidence.Tag != reference.Tag || evidence.Commit != reference.ProducerCommit || hex.EncodeToString(descriptorDigest[:]) != reference.DescriptorSHA256 || evidence.DescriptorEvidence.SHA256 != reference.DescriptorSHA256 {
		return fmt.Errorf("selection evidence identity differs from reviewed reference")
	}
	source, err := ParseReleaseManifest(evidence.Descriptor)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(source.OpenGrep, m.OpenGrep) {
		return fmt.Errorf("authenticated descriptor differs from the selected pins or source anchors")
	}
	if len(evidence.Artifacts) != len(m.OpenGrep.Artifacts) {
		return fmt.Errorf("selection evidence omits archive proofs")
	}
	proofs := append([]releaseSubjectEvidence{evidence.DescriptorEvidence}, evidence.Artifacts...)
	for index, proof := range proofs {
		if index > 0 && proof.SHA256 != m.OpenGrep.Artifacts[index-1].SHA256 {
			return fmt.Errorf("selection evidence archive differs from selected platform")
		}
		if err := verifyWorkflowClaims(proof.Workflow, reference.Tag, reference.ProducerCommit, proof.SHA256); err != nil {
			return err
		}
		if err := verifyReleaseClaims(proof.Release, proof.SHA256); err != nil {
			return err
		}
	}
	return nil
}
