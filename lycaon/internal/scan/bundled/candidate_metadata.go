package bundled

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxCandidateMetadataBytes = 8 << 20

type candidateProvenance struct {
	SchemaVersion    int    `json:"schema_version"`
	UpstreamRevision string `json:"upstream_revision"`
	SourceLockSHA256 string `json:"source_lock_sha256"`
	Platform         string `json:"platform"`
	Architecture     string `json:"architecture"`
	BinarySHA256     string `json:"binary_sha256"`
	BinaryBytes      int64  `json:"binary_bytes"`
	Version          string `json:"version"`
}

type candidateSourceLock struct {
	InterfacesRevision string            `json:"interfaces_revision"`
	Files              map[string]string `json:"files"`
	Grammars           []sourceGrammar   `json:"grammars"`
	Revision           string            `json:"revision"`
	UpstreamVersion    string            `json:"upstream_version"`
	PatchVersion       uint64            `json:"patch_version"`
}

func readCandidate(directory, goos, goarch string) (*localCandidate, error) {
	raw, err := readCandidateMetadata(filepath.Join(directory, "provenance.json"))
	if err != nil {
		return nil, err
	}
	c := &localCandidate{directory: directory, provenanceSHA256: candidateDigest(raw)}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&c.provenance); err != nil {
		return nil, fmt.Errorf("decode provenance: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("provenance must contain one JSON object")
	}
	lock, err := readCandidateMetadata(filepath.Join(directory, "source-lock.json"))
	if err != nil {
		return nil, err
	}
	if candidateDigest(lock) != c.provenance.SourceLockSHA256 {
		return nil, fmt.Errorf("source lock differs from provenance")
	}
	if err := json.Unmarshal(lock, &c.lock); err != nil {
		return nil, fmt.Errorf("decode source lock: %w", err)
	}
	if err := c.validateProvenance(goos, goarch); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *localCandidate) validateProvenance(goos, goarch string) error {
	p := c.provenance
	architecture := candidateArchitecture(goos, goarch)
	if p.SchemaVersion != 1 || p.Platform != goos || p.Architecture != architecture {
		return fmt.Errorf("artifact schema or platform does not match the selected target")
	}
	if !validHex(p.BinarySHA256, 32) || !validHex(p.SourceLockSHA256, 32) ||
		!validHex(p.UpstreamRevision, 20) || p.BinaryBytes <= 0 || p.BinaryBytes > maxArtifactBytes {
		return fmt.Errorf("candidate provenance has invalid binary or source identity")
	}
	if p.UpstreamRevision != c.lock.Revision || c.lock.PatchVersion == 0 ||
		!versionPattern.MatchString(c.lock.UpstreamVersion) ||
		p.Version != fmt.Sprintf("%s+paintedwolf.%d", c.lock.UpstreamVersion, c.lock.PatchVersion) {
		return fmt.Errorf("candidate version or base revision differs from source lock")
	}
	return nil
}

func candidateArchitecture(goos, goarch string) string {
	if goarch == "amd64" {
		return "x86_64"
	}
	if goos == "linux" && goarch == "arm64" {
		return "aarch64"
	}
	return goarch
}

func candidateRegularFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("candidate path is not a regular file: %s", path)
	}
	return info, nil
}

func readCandidateMetadata(path string) ([]byte, error) {
	info, err := candidateRegularFile(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxCandidateMetadataBytes {
		return nil, fmt.Errorf("candidate metadata exceeds size limit")
	}
	// #nosec G304 -- path is a fixed metadata name in the explicitly selected artifact.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxCandidateMetadataBytes+1))
	if err == nil && len(raw) > maxCandidateMetadataBytes {
		return nil, fmt.Errorf("candidate metadata exceeds size limit")
	}
	return raw, err
}

func candidateDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func verifyCandidateMetadata(path, expected string) error {
	raw, err := readCandidateMetadata(path)
	if err != nil {
		return err
	}
	if candidateDigest(raw) != expected {
		return fmt.Errorf("candidate metadata changed after selection")
	}
	return nil
}
