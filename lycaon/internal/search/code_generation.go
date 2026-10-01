package search

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// codeFile is one regular file the code engine may read.
type codeFile struct {
	root   CodeRoot
	rootID string
	rel    string
	abs    string
}

// codeGeneration pins a disk-backed metadata view for one search root.
type codeGeneration struct {
	root     CodeRoot
	rootID   string
	rootPath string
	reader   *sourcecatalog.IndexReader
}

func resolveCodeGeneration(ctx context.Context, catalog *sourcecatalog.Catalog, root CodeRoot, wait time.Duration) (codeGeneration, error) {
	rootPath := strings.TrimSpace(root.Path)
	rootID := strings.TrimSpace(root.RootID)
	if rootID == "" {
		return codeGeneration{}, errors.New("code root ID is required")
	}
	catalogRoot := sourcecatalog.Root{ID: rootID, Path: rootPath}
	reader, status, err := catalog.OpenIndex(ctx, root.ProjectID, catalogRoot, 0)
	if err == nil && reader == nil && wait > 0 {
		reader, status, err = catalog.OpenIndex(ctx, root.ProjectID, catalogRoot, wait)
	}
	if err != nil {
		return codeGeneration{}, err
	}
	if reader == nil {
		if status.State == sourcecatalog.StateFailed {
			return codeGeneration{}, fmt.Errorf("source catalog: %s", status.Error)
		}
		return codeGeneration{}, errCodeCatalogWarming
	}
	return codeGeneration{root: root, rootID: rootID, rootPath: rootPath, reader: reader}, nil
}

var errCodeCatalogWarming = errors.New("source catalog is warming")

// codeFilesFromEntries addresses each indexed file on disk; the projection
// returns only regular files.
func codeFilesFromEntries(root CodeRoot, rootID, rootPath string, entries []sourcecatalog.Entry) []codeFile {
	out := make([]codeFile, 0, len(entries))
	for _, entry := range entries {
		out = append(out, codeFile{root: root, rootID: rootID, rel: entry.Path,
			abs: filepath.Join(rootPath, filepath.FromSlash(entry.Path))})
	}
	return out
}

// codeIndexIncludeKey shares preparation across identical content projections.
func codeIndexIncludeKey(excludes dependencyDirs, maxBytes int64) string {
	names := make([]string, 0, len(excludes.names))
	for name := range excludes.names {
		names = append(names, name)
	}
	sort.Strings(names)
	prefixes := append([]string(nil), excludes.prefixes...)
	sort.Strings(prefixes)
	return strings.Join([]string{
		"search-content", strconv.FormatInt(maxBytes, 10),
		strings.Join(names, ","), strings.Join(prefixes, ","),
	}, "\x00")
}
