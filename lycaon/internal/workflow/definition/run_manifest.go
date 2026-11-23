package definition

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// runManifestV1 stores the resolved fields used at admission.
type runManifestV1 struct {
	Version  int      `yaml:"snapshot_version"`
	Manifest Manifest `yaml:"manifest"`
}

func EncodeRunManifest(manifest Manifest) (string, error) {
	raw, err := yaml.Marshal(runManifestV1{Version: 1, Manifest: manifest})
	return string(raw), err
}

func DecodeRunManifest(raw string) (Manifest, error) {
	var snapshot runManifestV1
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&snapshot); err != nil {
		return Manifest{}, err
	}
	if snapshot.Version != 1 {
		return Manifest{}, fmt.Errorf("unsupported workflow snapshot version %d", snapshot.Version)
	}
	if snapshot.Manifest.ID == "" || snapshot.Manifest.Version == "" {
		return Manifest{}, fmt.Errorf("missing workflow snapshot identity")
	}
	return snapshot.Manifest, nil
}
