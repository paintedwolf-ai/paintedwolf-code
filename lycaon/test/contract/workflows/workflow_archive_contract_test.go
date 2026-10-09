package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestWorkflowArchiveChecksums verifies that every sealed copy under archive/<workflow>/<version>/
// is intact, byte-identical to its recorded SHA256SUMS, and has no untracked files.
func TestWorkflowArchiveChecksums(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	packsRoot := filepath.Join(root, "lycaon", "config", "packs")

	var archiveDirs []string
	err := filepath.WalkDir(packsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && filepath.Base(filepath.Dir(filepath.Dir(path))) == "archive" {
			archiveDirs = append(archiveDirs, path)
			return filepath.SkipDir
		}
		return nil
	})
	contractcheck.FailErr(t, "walking packs for archive directories", err)

	if len(archiveDirs) == 0 {
		t.Fatalf("expected at least one archive directory under %s", packsRoot)
	}

	for _, archiveDir := range archiveDirs {
		t.Run(archiveDir, func(t *testing.T) {
			sumsPath := filepath.Join(archiveDir, "SHA256SUMS")
			sumsBytes, err := os.ReadFile(sumsPath)
			if err != nil {
				t.Fatalf("reading %s: %v", sumsPath, err)
			}

			lines := strings.Split(strings.TrimSpace(string(sumsBytes)), "\n")
			expectedSums := make(map[string]string)
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) != 2 {
					t.Fatalf("malformed SHA256SUMS line in %s: %q", sumsPath, line)
				}
				expectedSums[fields[1]] = fields[0]
			}

			if len(expectedSums) == 0 {
				t.Fatalf("%s has no checksum entries", sumsPath)
			}

			// Verify each file listed in SHA256SUMS matches.
			for relPath, expectedHex := range expectedSums {
				fullPath := filepath.Join(archiveDir, filepath.FromSlash(relPath))
				data, err := os.ReadFile(fullPath)
				if err != nil {
					t.Fatalf("reading file %s listed in SHA256SUMS: %v", fullPath, err)
				}
				sum := sha256.Sum256(data)
				actualHex := hex.EncodeToString(sum[:])
				if actualHex != expectedHex {
					t.Errorf("checksum mismatch for %s: got %s, want %s", relPath, actualHex, expectedHex)
				}
			}

			// Verify no extra files exist in archiveDir.
			err = filepath.WalkDir(archiveDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(archiveDir, path)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if rel == "SHA256SUMS" || strings.HasPrefix(filepath.Base(path), ".") {
					return nil
				}
				if _, ok := expectedSums[rel]; !ok {
					t.Errorf("file %s in %s is not recorded in SHA256SUMS", rel, archiveDir)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walking %s: %v", archiveDir, err)
			}
		})
	}
}
