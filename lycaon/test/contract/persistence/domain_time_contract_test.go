package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// A record stamp (updated_at, file mtime) is never a domain time. Only
// rebuildable caches may order or age their own entries by it.
var stampAgedCaches = map[string]string{
	"blobstore":      "reclaims unreferenced blobs past a grace period",
	"debugretention": "prunes debug captures by age",
	"sourceblob":     "reclaims unreferenced blobs past a grace period",
	"sourcecatalog":  "retires unused index generations",
	"webindex":       "evicts the least recently refreshed cached pages",
}

var (
	orderByUpdatedAt = regexp.MustCompile(`(?i)\bORDER\s+BY\b[^;\x60]*?\bupdated_at\b`)
	rangeOnUpdatedAt = regexp.MustCompile(`(?i)\b([a-zA-Z0-9_.]+\.)?updated_at\s*[<>]`)
	modTimeCompare   = regexp.MustCompile(`\.ModTime\(\)\.(Before|After)\(`)
)

func TestRecordStampsAreNeverDomainTime(t *testing.T) {
	t.Parallel()
	internalDir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal")
	var violations []string
	err := filepath.WalkDir(internalDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, "_test.go") {
			return err
		}
		isSQL := strings.HasSuffix(path, ".sql")
		if !isSQL && !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(internalDir, path)
		if err != nil {
			return err
		}
		if _, ok := stampAgedCaches[strings.Split(filepath.ToSlash(rel), "/")[0]]; ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		patterns := []*regexp.Regexp{orderByUpdatedAt, rangeOnUpdatedAt}
		if !isSQL {
			patterns = append(patterns, modTimeCompare)
		}
		for _, pattern := range patterns {
			if match := pattern.FindString(string(data)); match != "" {
				violations = append(violations, rel+": "+match)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal", err)
	contractcheck.FailViolations(t, "record stamps used as domain time", violations)
}
