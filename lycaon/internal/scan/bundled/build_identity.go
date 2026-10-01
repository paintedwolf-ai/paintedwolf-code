package bundled

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// buildIdentityBase64 is injected from the finalized native artifact by the build.
var buildIdentityBase64 string

// BuildIdentity binds the native executable and corresponding source payload.
type BuildIdentity struct {
	SchemaVersion        int               `json:"schema_version"`
	Version              string            `json:"version"`
	SourceLockSHA256     string            `json:"source_lock_sha256"`
	SourceManifestSHA256 string            `json:"source_manifest_sha256"`
	GOOS                 string            `json:"goos"`
	GOARCH               string            `json:"goarch"`
	BinarySHA256         string            `json:"binary_sha256"`
	BinaryBytes          int64             `json:"binary_bytes"`
	Payload              []PayloadIdentity `json:"payload"`
}

// PayloadIdentity binds a fixed artifact filename to its content.
type PayloadIdentity struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

var artifactPayloadNames = [...]string{
	"opengrep-source.tar.gz", "source-lock.json", "provenance.json", "LICENSE",
	"contracts.jsonl", "platform-checks.json", "runtime.json",
	"NOTICES-opengrep.md",
}

func (identity *BuildIdentity) validate(source OpenGrepManifest) error {
	if identity.SchemaVersion != 1 || identity.Version != source.Version || identity.SourceLockSHA256 != source.SourceLockSHA256 || identity.SourceManifestSHA256 != source.SourceManifestSHA256 {
		return fmt.Errorf("opengrep build identity differs from maintained source selection")
	}
	if !supportedPlatform(identity.GOOS, identity.GOARCH) || !validHex(identity.BinarySHA256, 32) || identity.BinaryBytes <= 0 || identity.BinaryBytes > maxArtifactBytes {
		return fmt.Errorf("invalid opengrep build platform or executable identity")
	}
	if len(identity.Payload) != len(artifactPayloadNames) {
		return fmt.Errorf("opengrep build identity requires complete source and qualification payload")
	}
	for i, name := range artifactPayloadNames {
		part := identity.Payload[i]
		if part.Name != name || !validHex(part.SHA256, 32) || part.Bytes <= 0 || part.Bytes > maxArtifactBytes {
			return fmt.Errorf("invalid opengrep payload identity for %s", name)
		}
		if name == "source-lock.json" && part.SHA256 != source.SourceLockSHA256 {
			return fmt.Errorf("opengrep source payload differs from source selection")
		}
	}
	return nil
}

// ManifestWithBuildIdentity applies the identity embedded at build time.
func ManifestWithBuildIdentity(source *Manifest, encoded string) (*Manifest, error) {
	if err := ValidateManifest(source); err != nil {
		return nil, err
	}
	if source.candidate != nil {
		return nil, fmt.Errorf("experimental candidate cannot receive shipping build identity")
	}
	if len(encoded) > 32<<10 {
		return nil, fmt.Errorf("opengrep build identity exceeds size limit")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode opengrep build identity: %w", err)
	}
	var identity BuildIdentity
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil {
		return nil, fmt.Errorf("parse opengrep build identity: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("opengrep build identity must contain one JSON object")
	}
	if err := identity.validate(source.OpenGrep); err != nil {
		return nil, err
	}
	return &Manifest{OpenGrep: source.OpenGrep, OpenGrepSelection: source.OpenGrepSelection, identity: &identity}, nil
}

// EncodedBuildIdentity emits the authority for a verified, finalized build artifact.
func EncodedBuildIdentity(m *Manifest) (string, error) {
	if err := ValidateManifest(m); err != nil {
		return "", err
	}
	if m.identity == nil || m.artifactDirectory == "" {
		return "", fmt.Errorf("a verified build artifact is required to emit identity")
	}
	if err := verifyArtifactDirectory(m, m.artifactDirectory); err != nil {
		return "", err
	}
	raw, err := json.Marshal(m.identity)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
