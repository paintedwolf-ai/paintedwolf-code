package extpacks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// PackageBodyMetadata identifies one immutable cached body.
type PackageBodyMetadata struct {
	PackID           string   `json:"pack_id"`
	Version          string   `json:"version"`
	ResolvedRevision string   `json:"resolved_revision"`
	Integrity        string   `json:"integrity"`
	ExtensionAPI     string   `json:"extension_api"`
	Kind             PackKind `json:"kind"`
}

func WritePackageBodyMetadata(packRoot string, meta PackageBodyMetadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return writeCacheMetadataFile(filepath.Join(packRoot, PackageBodyMetadataName), append(data, '\n'))
}

func ReadPackageBodyMetadata(packRoot string) (PackageBodyMetadata, error) {
	data, err := os.ReadFile(filepath.Join(packRoot, PackageBodyMetadataName))
	if err != nil {
		return PackageBodyMetadata{}, err
	}
	var meta PackageBodyMetadata
	if err := decodeStrictMetadata(data, &meta); err != nil {
		return PackageBodyMetadata{}, fmt.Errorf("pack metadata: %w", err)
	}
	return meta, nil
}

func decodeStrictMetadata(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
