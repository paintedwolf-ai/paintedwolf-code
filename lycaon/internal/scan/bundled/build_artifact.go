package bundled

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type artifactAttestations struct {
	SourceArchiveSHA256  string `json:"source_archive_sha256"`
	SourceArchiveBytes   int64  `json:"source_archive_bytes"`
	ContractsSHA256      string `json:"contracts_sha256"`
	PlatformChecksSHA256 string `json:"platform_checks_sha256"`
	RuntimeSHA256        string `json:"runtime_sha256"`
}

// SelectBuildArtifact validates artifact provenance against the selected source digests.
// Runtime resolution uses only the identity embedded in the sidecar.
func SelectBuildArtifact(source *Manifest, directory, goos, goarch string) (*Manifest, error) {
	if err := ValidateManifest(source); err != nil {
		return nil, err
	}
	if source.candidate != nil {
		return nil, fmt.Errorf("build artifact cannot be combined with experimental candidate")
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("build artifact directory must be absolute")
	}
	candidate, err := readCandidate(filepath.Clean(directory), goos, goarch)
	if err != nil {
		return nil, fmt.Errorf("read build artifact: %w", err)
	}
	p := candidate.provenance
	if p.Version != source.OpenGrep.Version || p.SourceLockSHA256 != source.OpenGrep.SourceLockSHA256 || candidate.lock.Revision != source.OpenGrep.BaseRevision || candidate.lock.UpstreamVersion != source.OpenGrep.UpstreamVersion || candidate.lock.PatchVersion != source.OpenGrep.Revision {
		return nil, fmt.Errorf("build artifact differs from maintained source selection")
	}
	identity := &BuildIdentity{SourceManifestSHA256: source.OpenGrep.SourceManifestSHA256, SchemaVersion: 1, Version: p.Version, SourceLockSHA256: p.SourceLockSHA256, GOOS: goos, GOARCH: goarch, BinarySHA256: p.BinarySHA256, BinaryBytes: p.BinaryBytes}
	for _, name := range artifactPayloadNames {
		part, err := identifyPayload(filepath.Join(directory, name), name)
		if err != nil {
			return nil, err
		}
		identity.Payload = append(identity.Payload, part)
	}
	m := &Manifest{OpenGrep: source.OpenGrep, OpenGrepSelection: source.OpenGrepSelection, identity: identity, artifactDirectory: filepath.Clean(directory)}
	if err := ValidateManifest(m); err != nil {
		return nil, err
	}
	if err := verifyArtifactDirectory(m, directory); err != nil {
		return nil, err
	}
	if err := verifyBuildAttestations(m, candidate.provenanceSHA256); err != nil {
		return nil, err
	}
	proof, err := verifySourceArchive(m, candidate.lock)
	if err != nil {
		return nil, err
	}
	if err := verifyQualification(m, candidate.lock, proof); err != nil {
		return nil, err
	}
	return m, nil
}

func identifyPayload(path, name string) (PayloadIdentity, error) {
	info, err := candidateRegularFile(path)
	if err != nil {
		return PayloadIdentity{}, err
	}
	if info.Size() <= 0 || info.Size() > maxArtifactBytes {
		return PayloadIdentity{}, fmt.Errorf("artifact payload size is invalid: %s", name)
	}
	// #nosec G304 -- path is a fixed file in the selected build directory.
	file, err := os.Open(path)
	if err != nil {
		return PayloadIdentity{}, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, maxArtifactBytes+1))
	if err != nil {
		return PayloadIdentity{}, err
	}
	if count != info.Size() {
		return PayloadIdentity{}, fmt.Errorf("artifact changed during selection: %s", name)
	}
	return PayloadIdentity{Name: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: count}, nil
}

func verifyBuildAttestations(m *Manifest, provenanceHash string) error {
	raw, err := readCandidateMetadata(filepath.Join(m.artifactDirectory, "provenance.json"))
	if err != nil {
		return err
	}
	if candidateDigest(raw) != provenanceHash {
		return fmt.Errorf("artifact provenance changed during selection")
	}
	var attestations artifactAttestations
	if err := json.Unmarshal(raw, &attestations); err != nil {
		return err
	}
	expected := map[string]string{"source-lock.json": m.OpenGrep.SourceLockSHA256, "provenance.json": provenanceHash, "opengrep-source.tar.gz": attestations.SourceArchiveSHA256, "contracts.jsonl": attestations.ContractsSHA256, "platform-checks.json": attestations.PlatformChecksSHA256, "runtime.json": attestations.RuntimeSHA256}
	for _, part := range m.identity.Payload {
		if digest, ok := expected[part.Name]; ok && part.SHA256 != digest {
			return fmt.Errorf("artifact %s differs from build provenance", part.Name)
		}
		if part.Name == "opengrep-source.tar.gz" && part.Bytes != attestations.SourceArchiveBytes {
			return fmt.Errorf("source archive size differs from build provenance")
		}
	}
	return nil
}

func verifyArtifactDirectory(m *Manifest, directory string) error {
	art, err := m.ArtifactForPlatform(m.identity.GOOS, m.identity.GOARCH)
	if err != nil {
		return err
	}
	binary := filepath.Join(directory, filepath.Base(ArtifactPath("", m.OpenGrep.Version, art.GOOS)))
	if err := verifyArtifactFile(binary, art.SHA256, art.BinaryBytes, true, art.GOOS); err != nil {
		return err
	}
	for _, part := range m.identity.Payload {
		if err := verifyArtifactFile(filepath.Join(directory, part.Name), part.SHA256, part.Bytes, false, art.GOOS); err != nil {
			return err
		}
	}
	return nil
}

func verifyArtifactFile(path, digest string, size int64, executable bool, goos string) error {
	info, err := candidateRegularFile(path)
	if err != nil {
		return err
	}
	if info.Size() != size || (executable && goos != "windows" && info.Mode().Perm()&0o111 == 0) {
		return fmt.Errorf("artifact size or executable permissions differ: %s", path)
	}
	if err := VerifyFileSHA256(path, digest); err != nil {
		return fmt.Errorf("%w: %w", ErrOpenGrepChecksum, err)
	}
	return nil
}

// VerifyStagedArtifact checks every staged byte against the sidecar's build authority.
func VerifyStagedArtifact(m *Manifest, root, goos, goarch string) (string, error) {
	if _, err := m.ArtifactForPlatform(goos, goarch); err != nil {
		return "", err
	}
	if m.identity == nil {
		return "", fmt.Errorf("staged source verification requires shipping build identity")
	}
	if err := verifyStageDirectories(root, m.OpenGrep.Version); err != nil {
		return "", err
	}
	if err := verifyArtifactDirectory(m, EngineBundledDir(root, m.OpenGrep.Version)); err != nil {
		return "", err
	}
	return ArtifactPath(root, m.OpenGrep.Version, goos), nil
}
