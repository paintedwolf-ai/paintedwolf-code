package git

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/gitrepo"
)

func fileHistoryCacheKey(dir string, opts GitFileHistoryOpts) (string, bool) {
	repo, ok := gitrepo.Discover(dir)
	if !ok || repo.GitDir == "" || repo.CommonDir == "" {
		return "", false
	}
	// Shallow boundaries and grafts change ancestry without changing object IDs.
	for _, base := range []string{repo.GitDir, repo.CommonDir} {
		for _, name := range []string{"shallow", "info/grafts"} {
			if _, err := os.Stat(filepath.Join(base, name)); !os.IsNotExist(err) {
				return "", false
			}
		}
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d", dir,
		repo.GitDir, repo.CommonDir, opts.Revision, opts.Path, opts.Skip, opts.Limit), true
}

func fileHistoryBytes(key string, rows []GitFileCommit) int {
	bytes := len(key) + 128
	for _, row := range rows {
		bytes += 160 + len(row.Hash) + len(row.AuthorName) + len(row.Subject) + len(row.Path) + len(row.BlobOID)
	}
	return bytes
}
