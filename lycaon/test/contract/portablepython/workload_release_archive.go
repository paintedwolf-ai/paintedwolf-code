package portablepython

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// GradeReleaseIndex verifies release-index.json against the original release anchor JSON inputs.
func GradeReleaseIndex(anchors map[string][]byte, indexJSON []byte) error {
	var parsed struct {
		Schema   string                                 `json:"schema"`
		Versions map[string]map[string]map[string]any `json:"versions"`
	}
	if err := json.Unmarshal(indexJSON, &parsed); err != nil {
		return fmt.Errorf("parse release-index.json: %w", err)
	}
	count := 0
	for name, raw := range anchors {
		var a struct {
			Kind       string   `json:"kind"`
			Version    string   `json:"version"`
			SHA256Sums string   `json:"sha256sums"`
			Holdout    []string `json:"holdout"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return fmt.Errorf("parse anchor %s: %w", name, err)
		}
		entry, ok := parsed.Versions[a.Version][a.Kind]
		if !ok {
			return fmt.Errorf("missing version=%s kind=%s in release-index.json", a.Version, a.Kind)
		}
		if entry["sha256sums"] != a.SHA256Sums {
			return fmt.Errorf("sha256sums mismatch for %s/%s", a.Version, a.Kind)
		}
		count++
	}
	if count == 0 {
		return fmt.Errorf("no anchors provided")
	}
	return nil
}

// GradeReleaseArchive verifies byte-identical reproducibility, exact releases/<filename> members, and root SHA256SUMS.
func GradeReleaseArchive(anchors map[string][]byte, zipA, zipB []byte) error {
	if !bytes.Equal(zipA, zipB) {
		return fmt.Errorf("archive builds are not byte-identical")
	}
	zr, err := zip.NewReader(bytes.NewReader(zipA), int64(len(zipA)))
	if err != nil {
		return fmt.Errorf("open candidate zip: %w", err)
	}
	members := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
		members[f.Name] = data
	}
	if len(members) != len(anchors)+1 {
		return fmt.Errorf("member count mismatch: want %d (%d anchors + SHA256SUMS), got %d", len(anchors)+1, len(anchors), len(members))
	}
	sumsBytes, ok := members["SHA256SUMS"]
	if !ok {
		return fmt.Errorf("missing root SHA256SUMS member in archive")
	}
	names := make([]string, 0, len(anchors))
	for name := range anchors {
		names = append(names, name)
	}
	sort.Strings(names)
	var expectedSums strings.Builder
	for _, base := range names {
		memberName := "releases/" + path.Base(base)
		got, ok := members[memberName]
		if !ok {
			return fmt.Errorf("missing expected member %q (flattened paths are forbidden)", memberName)
		}
		if !bytes.Equal(got, anchors[base]) {
			return fmt.Errorf("byte mismatch for archive member %s", memberName)
		}
		sum := sha256.Sum256(anchors[base])
		fmt.Fprintf(&expectedSums, "%s  %s\n", hex.EncodeToString(sum[:]), memberName)
	}
	if string(sumsBytes) != expectedSums.String() {
		return fmt.Errorf("SHA256SUMS mismatch:\nwant:\n%s\ngot:\n%s", expectedSums.String(), string(sumsBytes))
	}
	return nil
}
