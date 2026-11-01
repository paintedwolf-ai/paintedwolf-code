package modelstore

import (
	"fmt"
)

func validate(m Manifest) error {
	if !nameShape.MatchString(m.Model) {
		return fmt.Errorf("modelstore: model name %q is not a safe identity", m.Model)
	}
	if !nameShape.MatchString(m.Revision) {
		return fmt.Errorf("modelstore: revision %q is not a safe identity", m.Revision)
	}
	if !nameShape.MatchString(m.Device) {
		return fmt.Errorf("modelstore: device %q is not a safe identity", m.Device)
	}
	if err := validateArtifact(m.Artifact); err != nil {
		return err
	}
	if len(m.Files) > 64 {
		return fmt.Errorf("modelstore: too many supplementary artifacts")
	}
	seen := map[string]bool{m.Artifact.Digest: true}
	names := map[string]bool{artifactFileName(m.Artifact, true): true}
	for _, f := range m.Files {
		if err := validateArtifact(f); err != nil {
			return err
		}
		if seen[f.Digest] {
			return fmt.Errorf("modelstore: duplicate artifact entry %s", f.Digest)
		}
		seen[f.Digest] = true
		name := artifactFileName(f, false)
		if names[name] {
			return fmt.Errorf("modelstore: duplicate artifact filename %s", name)
		}
		names[name] = true
	}
	return nil
}

func validateArtifact(a Artifact) error {
	if a.Name != "" && !nameShape.MatchString(a.Name) {
		return fmt.Errorf("modelstore: artifact name %q is not a safe identity", a.Name)
	}
	if !digestShape.MatchString(a.Digest) {
		return fmt.Errorf("modelstore: digest %q is not a hex sha256", a.Digest)
	}
	if a.Bytes <= 0 || a.Bytes > MaxArtifactBytes {
		return fmt.Errorf("modelstore: artifact size %d out of bounds", a.Bytes)
	}
	return nil
}

func artifactFileName(a Artifact, primary bool) string {
	if a.Name != "" {
		return a.Name
	}
	switch a.Format {
	case "gguf":
		return "model.gguf"
	case "safetensors":
		return "model.safetensors"
	case "config", "config.json":
		return "config.json"
	case "tokenizer", "tokenizer.json":
		return "tokenizer.json"
	}
	if primary {
		return "model.safetensors"
	}
	if nameShape.MatchString(a.Format) {
		return a.Format
	}
	return a.Digest
}
