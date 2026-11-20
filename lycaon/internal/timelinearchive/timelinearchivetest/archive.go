package timelinearchivetest

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	"io/fs"
)

// Unpack inspects every entry produced by a timeline recording fixture.
func Unpack(archive []byte) (timelinearchive.Manifest, map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return timelinearchive.Manifest{}, nil, err
	}
	raw, err := fs.ReadFile(zr, "manifest.json")
	if err != nil {
		return timelinearchive.Manifest{}, nil, err
	}
	var manifest timelinearchive.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return timelinearchive.Manifest{}, nil, err
	}
	if manifest.Version != timelinearchive.FormatVersion {
		return timelinearchive.Manifest{}, nil, fmt.Errorf("unexpected timeline version %d", manifest.Version)
	}
	entries := make(map[string][]byte, len(zr.File))
	for _, entry := range zr.File {
		body, err := fs.ReadFile(zr, entry.Name)
		if err != nil {
			return timelinearchive.Manifest{}, nil, err
		}
		entries[entry.Name] = body
	}
	return manifest, entries, nil
}
