package bundled

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// ReleaseSelectionReference is consumer-authored evidence metadata, excluded from producer descriptors.
type ReleaseSelectionReference struct {
	Tag              string `yaml:"tag" json:"tag"`
	ProducerCommit   string `yaml:"producer_commit" json:"producer_commit"`
	DescriptorSHA256 string `yaml:"descriptor_sha256" json:"descriptor_sha256"`
	EvidenceSHA256   string `yaml:"evidence_sha256" json:"evidence_sha256"`
}

func (reference ReleaseSelectionReference) validate(version string) error {
	if reference.Tag != "v"+version || !validHex(reference.ProducerCommit, 20) || !validHex(reference.DescriptorSHA256, 32) || !validHex(reference.EvidenceSHA256, 32) {
		return fmt.Errorf("invalid consumer release selection reference")
	}
	return nil
}

func (reference ReleaseSelectionReference) evidenceFile() string {
	return filepath.Join("opengrep-attestations", reference.DescriptorSHA256+".json")
}

type releaseSubjectEvidence struct {
	SHA256   string          `json:"sha256"`
	Workflow json.RawMessage `json:"workflow_verification"`
	Release  json.RawMessage `json:"release_verification"`
}

type releaseSelectionEvidence struct {
	Tag                string                   `json:"tag"`
	Commit             string                   `json:"expected_commit"`
	Descriptor         []byte                   `json:"descriptor_bytes"`
	DescriptorEvidence releaseSubjectEvidence   `json:"descriptor_evidence"`
	Artifacts          []releaseSubjectEvidence `json:"artifacts"`
	ReleaseState       json.RawMessage          `json:"release_state"`
}

// Evidence storage precedes publication of its manifest digest.
func persistReleaseEvidence(destination string, evidence releaseSelectionEvidence) (*ReleaseSelectionReference, error) {
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > maxReleaseEvidenceBytes {
		return nil, fmt.Errorf("release authentication evidence exceeds limit")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	reference := &ReleaseSelectionReference{Tag: evidence.Tag, ProducerCommit: evidence.Commit, DescriptorSHA256: evidence.DescriptorEvidence.SHA256, EvidenceSHA256: hex.EncodeToString(digest[:])}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: filepath.Dir(absolute), Rel: reference.evidenceFile()},
		Source:   bytes.NewReader(raw), Mode: 0o644,
	})
	return reference, err
}
