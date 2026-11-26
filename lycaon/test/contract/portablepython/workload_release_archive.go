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
	"strings"
)

// GradeReleaseIndex verifies release-index.json has exact membership matching the release anchor JSONs.
func GradeReleaseIndex(anchors map[string][]byte, indexJSON []byte) error {
	if len(anchors) == 0 {
		return fmt.Errorf("no anchors provided")
	}
	var parsed struct {
		Schema   string                                 `json:"schema"`
		Versions map[string]map[string]map[string]any `json:"versions"`
	}
	if err := json.Unmarshal(indexJSON, &parsed); err != nil {
		return fmt.Errorf("parse release-index.json: %w", err)
	}
	expectedPairs := make(map[string]bool, len(anchors))
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
		expectedPairs[a.Version+"\x00"+a.Kind] = true
	}
	for ver, kinds := range parsed.Versions {
		if len(kinds) == 0 {
			return fmt.Errorf("unexpected empty version entry %q in release-index.json", ver)
		}
		for kind := range kinds {
			if !expectedPairs[ver+"\x00"+kind] {
				return fmt.Errorf("unexpected extra release-index entry version=%s kind=%s", ver, kind)
			}
		}
	}
	return nil
}

func parseSHA256Sums(raw []byte) (map[string]string, error) {
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return nil, fmt.Errorf("SHA256SUMS is empty")
	}
	out := make(map[string]string)
	for i, line := range strings.Split(text, "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 || len(parts[0]) != 64 || parts[1] == "" {
			return nil, fmt.Errorf("malformed SHA256SUMS line %d: %q", i+1, line)
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return nil, fmt.Errorf("invalid hex digest on SHA256SUMS line %d: %w", i+1, err)
		}
		if _, dup := out[parts[1]]; dup {
			return nil, fmt.Errorf("duplicate path %q in SHA256SUMS", parts[1])
		}
		out[parts[1]] = strings.ToLower(parts[0])
	}
	return out, nil
}

// GradeReleaseArchive verifies byte-identical reproducibility, no duplicate ZIP entries,
// exact releases/<filename> members, and semantic SHA256SUMS membership at root.
func GradeReleaseArchive(anchors map[string][]byte, zipA, zipB []byte) error {
	if len(anchors) == 0 {
		return fmt.Errorf("no anchors provided")
	}
	if !bytes.Equal(zipA, zipB) {
		return fmt.Errorf("archive builds are not byte-identical")
	}
	zr, err := zip.NewReader(bytes.NewReader(zipA), int64(len(zipA)))
	if err != nil {
		return fmt.Errorf("open candidate zip: %w", err)
	}
	members := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		if _, dup := members[f.Name]; dup {
			return fmt.Errorf("duplicate ZIP entry name %q", f.Name)
		}
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
	gotSums, err := parseSHA256Sums(sumsBytes)
	if err != nil {
		return err
	}
	if len(gotSums) != len(anchors) {
		return fmt.Errorf("SHA256SUMS entry count mismatch: want %d, got %d", len(anchors), len(gotSums))
	}
	for base, raw := range anchors {
		memberName := "releases/" + path.Base(base)
		got, ok := members[memberName]
		if !ok {
			return fmt.Errorf("missing expected member %q (flattened paths are forbidden)", memberName)
		}
		if !bytes.Equal(got, raw) {
			return fmt.Errorf("byte mismatch for archive member %s", memberName)
		}
		sum := sha256.Sum256(raw)
		wantHex := hex.EncodeToString(sum[:])
		if gotSums[memberName] != wantHex {
			return fmt.Errorf("SHA256SUMS mismatch for %s: want %s, got %q", memberName, wantHex, gotSums[memberName])
		}
	}
	return nil
}
