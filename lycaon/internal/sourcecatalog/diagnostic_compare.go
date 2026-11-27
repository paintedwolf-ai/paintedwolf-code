package sourcecatalog

import (
	"context"
	"database/sql"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func subtreePrefixBounds(subtree string) (string, string) {
	return subtree + "/", subtree + "0"
}

// readBoundedDiagnosticCatalogSnapshot reads ONLY ancestor boundaries plus
// bounded subtree rows using the unique path index (no global count(*) scans).
func readBoundedDiagnosticCatalogSnapshot(
	ctx context.Context,
	tx *sql.Tx,
	status TreeStatus,
	subtree string,
	maxEntries int,
) (IndexCoverage, map[string]struct{}, map[string]string, map[string]catalogNodeRecord, string, int, bool, error) {
	rowsRead := 0
	coverage := CoverageFromStatus(status)
	faults := make(map[string]struct{})
	refusedDirs := make(map[string]string)

	// 1. Read ancestor boundary and fault rows for the selected subtree only.
	ancestors := indexAncestors(subtree)
	if len(ancestors) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ancestors)), ",")
		args := make([]any, len(ancestors))
		for i, a := range ancestors {
			args[i] = a
		}
		// #nosec G202 -- Fixed placeholder list with bound ancestor paths.
		fRows, qErr := tx.QueryContext(ctx, "SELECT path FROM faults WHERE path IN ("+placeholders+")", args...)
		if qErr != nil {
			return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, qErr
		}
		for fRows.Next() {
			var p string
			if err := fRows.Scan(&p); err != nil {
				_ = fRows.Close()
				return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
			}
			faults[p] = struct{}{}
			rowsRead++
		}
		if err := fRows.Close(); err != nil {
			return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
		}

		// #nosec G202 -- Fixed placeholder list with bound ancestor paths.
		rRows, qErr := tx.QueryContext(ctx, "SELECT path, refused FROM nodes WHERE refused<>'' AND path IN ("+placeholders+")", args...)
		if qErr != nil {
			return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, qErr
		}
		for rRows.Next() {
			var p, ref string
			if err := rRows.Scan(&p, &ref); err != nil {
				_ = rRows.Close()
				return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
			}
			refusedDirs[p] = ref
			rowsRead++
		}
		if err := rRows.Close(); err != nil {
			return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
		}
	}

	// 2. Read bounded faults inside the selected subtree.
	limit := maxEntries + 1
	var subFaultRows *sql.Rows
	var err error
	if subtree == "." {
		subFaultRows, err = tx.QueryContext(ctx, "SELECT path FROM faults ORDER BY path LIMIT ?", limit)
	} else {
		lo, hi := subtreePrefixBounds(subtree)
		subFaultRows, err = tx.QueryContext(ctx, "SELECT path FROM faults WHERE path=? OR (path>=? AND path<?) ORDER BY path LIMIT ?", subtree, lo, hi, limit)
	}
	if err != nil {
		return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
	}
	for subFaultRows.Next() {
		var p string
		if err := subFaultRows.Scan(&p); err != nil {
			_ = subFaultRows.Close()
			return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
		}
		faults[p] = struct{}{}
		rowsRead++
	}
	if err := subFaultRows.Close(); err != nil {
		return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
	}

	// 3. Read bounded nodes inside the selected subtree using the unique path index.
	entries := make(map[string]catalogNodeRecord)
	var nodeRows *sql.Rows
	if subtree == "." {
		nodeRows, err = tx.QueryContext(ctx, "SELECT path, is_dir, symlink, regular, refused, indexed FROM nodes WHERE path<>'.' ORDER BY path LIMIT ?", limit)
	} else {
		lo, hi := subtreePrefixBounds(subtree)
		nodeRows, err = tx.QueryContext(ctx, "SELECT path, is_dir, symlink, regular, refused, indexed FROM nodes WHERE path=? OR (path>=? AND path<?) ORDER BY path LIMIT ?", subtree, lo, hi, limit)
	}
	if err != nil {
		return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
	}
	scannedNodes := 0
	maxCatalogPath := ""
	for nodeRows.Next() {
		var p, refused string
		var isDir, isSymlink, isRegular, indexed bool
		if err := nodeRows.Scan(&p, &isDir, &isSymlink, &isRegular, &refused, &indexed); err != nil {
			_ = nodeRows.Close()
			return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
		}
		rowsRead++
		scannedNodes++
		if refused != "" {
			refusedDirs[p] = refused
		}
		if p == "." || !indexed || scannedNodes > maxEntries {
			continue
		}
		if p > maxCatalogPath {
			maxCatalogPath = p
		}
		entries[p] = catalogNodeRecord{
			path:      p,
			kind:      typedEntryKind(isDir, isSymlink, isRegular),
			refused:   refused,
			indexed:   indexed,
			isDir:     isDir,
			isSymlink: isSymlink,
		}
	}
	if err := nodeRows.Close(); err != nil {
		return IndexCoverage{}, nil, nil, nil, "", rowsRead, false, err
	}

	// Derive scoped coverage counts from the bounded sample (no global table scan).
	coverage.FailedDirectories = len(faults)
	for _, reason := range refusedDirs {
		if reason != "" && reason != "lazy" {
			coverage.BoundedDirectories++
		}
	}

	return coverage, faults, refusedDirs, entries, maxCatalogPath, rowsRead, scannedNodes > maxEntries, nil
}

func walkDirectSubtree(
	ctx context.Context,
	rootPath string,
	cfg DiagnosticConfig,
	policy walkPolicy,
	faults map[string]struct{},
	refusedDirs map[string]string,
) (map[string]diskNodeRecord, map[string]struct{}, map[string]DirectoryStamp, DiagnosticReadCounts, bool, error) {
	return walkDirectSubtreeWithStat(ctx, rootPath, cfg, policy, faults, refusedDirs, (*os.File).Stat)
}

func walkDirectSubtreeWithStat(
	ctx context.Context,
	rootPath string,
	cfg DiagnosticConfig,
	policy walkPolicy,
	faults map[string]struct{},
	refusedDirs map[string]string,
	statDir func(*os.File) (os.FileInfo, error),
) (map[string]diskNodeRecord, map[string]struct{}, map[string]DirectoryStamp, DiagnosticReadCounts, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, DiagnosticReadCounts{}, false, err
	}
	if statDir == nil {
		statDir = (*os.File).Stat
	}
	osRoot, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, nil, DiagnosticReadCounts{}, false, err
	}
	defer func() { _ = osRoot.Close() }()

	type queueItem struct {
		dir   string
		depth int
	}

	diskEntries := make(map[string]diskNodeRecord)
	fullSampledDirs := make(map[string]struct{})
	dirStamps := make(map[string]DirectoryStamp)
	var reads DiagnosticReadCounts
	truncated := false

	queue := []queueItem{{dir: cfg.Subtree, depth: 0}}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, reads, truncated, err
		}
		item := queue[0]
		queue = queue[1:]

		if reads.DirAttempts >= cfg.MaxDirs || reads.EntryReads >= cfg.MaxEntries {
			truncated = true
			break
		}
		if hasDeclaredDirGap(item.dir, faults, refusedDirs, policy) {
			continue
		}

		// Charge MaxDirs at the actual I/O boundary so failed opens cannot bypass the cap.
		reads.DirAttempts++
		f, openErr := osRoot.Open(filepath.FromSlash(item.dir))
		if openErr != nil {
			continue
		}
		dirInfo, statErr := statDir(f)
		remaining := cfg.MaxEntries - reads.EntryReads
		// Bound the underlying ReadDir syscall and working set to remaining+1 entries.
		dirEntries, readErr := f.ReadDir(remaining + 1)
		_ = f.Close()
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, reads, truncated, err
		}
		if readErr != nil && readErr != io.EOF {
			continue
		}
		reads.DirReads++
		var stamp DirectoryStamp
		if statErr == nil && dirInfo != nil {
			stamp = directoryStampOf(dirInfo)
		}
		// A directory whose stamp cannot be captured must not authorize stale/missing assertions.
		if statErr != nil || !stamp.known() {
			truncated = true
			continue
		}
		dirStamps[item.dir] = stamp

		dirFullyRead := len(dirEntries) <= remaining
		if !dirFullyRead {
			truncated = true
			dirEntries = dirEntries[:remaining]
		}
		if dirFullyRead {
			fullSampledDirs[item.dir] = struct{}{}
		}

		sort.Slice(dirEntries, func(i, j int) bool { return dirEntries[i].Name() < dirEntries[j].Name() })
		for _, de := range dirEntries {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, reads, truncated, err
			}
			reads.EntryReads++
			name := de.Name()
			rel := name
			if item.dir != "." {
				rel = path.Join(item.dir, name)
			}
			if skippedIndexPath(rel) {
				continue
			}

			mode := de.Type()
			isSymlink := mode&fs.ModeSymlink != 0
			isDir := mode.IsDir()
			isRegular := mode.IsRegular()
			if mode == 0 {
				info, lstatErr := osRoot.Lstat(filepath.FromSlash(rel))
				reads.MetadataReads++
				if lstatErr != nil {
					continue
				}
				m := info.Mode()
				isSymlink = m&fs.ModeSymlink != 0
				isDir = m.IsDir()
				isRegular = m.IsRegular()
			}

			if !policy.selects(rel, isDir) {
				continue
			}

			diskEntries[rel] = diskNodeRecord{
				path:  rel,
				kind:  typedEntryKind(isDir, isSymlink, isRegular),
				isDir: isDir,
			}

			if isDir && !isSymlink && item.depth+1 < cfg.MaxDepth {
				if !hasDeclaredDirGap(rel, faults, refusedDirs, policy) {
					queue = append(queue, queueItem{dir: rel, depth: item.depth + 1})
				}
			}
		}
	}

	return diskEntries, fullSampledDirs, dirStamps, reads, truncated, nil
}

func compareCatalogAndDisk(
	catalogEntries map[string]catalogNodeRecord,
	diskEntries map[string]diskNodeRecord,
	fullSampledDirs map[string]struct{},
	faults map[string]struct{},
	refusedDirs map[string]string,
	policy walkPolicy,
	truncatedWalk bool,
	catalogTruncated bool,
	maxCatalogPath string,
) []DiagnosticDisagreement {
	var out []DiagnosticDisagreement

	diskPaths := make([]string, 0, len(diskEntries))
	for p := range diskEntries {
		diskPaths = append(diskPaths, p)
	}
	sort.Strings(diskPaths)

	for _, p := range diskPaths {
		disk := diskEntries[p]
		if hasDeclaredAncestorGap(p, faults, refusedDirs, policy) {
			continue
		}
		cat, ok := catalogEntries[p]
		if !ok {
			// Symmetric truncation guard: if the bounded SQL sample truncated, any path
			// at or past the SQL frontier (or when frontier is empty) was never proven absent.
			if catalogTruncated && (maxCatalogPath == "" || p >= maxCatalogPath) {
				continue
			}
			out = append(out, DiagnosticDisagreement{
				Path:     p,
				Kind:     DisagreementMissingInCatalog,
				DiskKind: disk.kind,
			})
			continue
		}
		if cat.kind != disk.kind {
			out = append(out, DiagnosticDisagreement{
				Path:        p,
				Kind:        DisagreementKindMismatch,
				CatalogKind: cat.kind,
				DiskKind:    disk.kind,
			})
		}
	}

	catPaths := make([]string, 0, len(catalogEntries))
	for p := range catalogEntries {
		catPaths = append(catPaths, p)
	}
	sort.Strings(catPaths)

	for _, p := range catPaths {
		if _, ok := diskEntries[p]; ok {
			continue
		}
		parent := normalizeDir(path.Dir(p))
		// Only report extra_in_catalog when the parent directory was fully read on disk.
		if _, fullySampled := fullSampledDirs[parent]; !fullySampled {
			continue
		}
		if truncatedWalk {
			continue
		}
		if hasDeclaredAncestorGap(p, faults, refusedDirs, policy) {
			continue
		}
		cat := catalogEntries[p]
		out = append(out, DiagnosticDisagreement{
			Path:        p,
			Kind:        DisagreementExtraInCatalog,
			CatalogKind: cat.kind,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func hasDeclaredDirGap(dir string, faults map[string]struct{}, refusedDirs map[string]string, policy walkPolicy) bool {
	if skippedIndexPath(dir) {
		return true
	}
	if _, faulted := faults[dir]; faulted {
		return true
	}
	if reason, refused := refusedDirs[dir]; refused && reason != "" {
		return true
	}
	if policy.boundaryPath(dir, true) != "" {
		return true
	}
	return false
}

func hasDeclaredAncestorGap(rel string, faults map[string]struct{}, refusedDirs map[string]string, policy walkPolicy) bool {
	if skippedIndexPath(rel) {
		return true
	}
	for _, anc := range indexAncestors(normalizeDir(path.Dir(rel))) {
		if hasDeclaredDirGap(anc, faults, refusedDirs, policy) {
			return true
		}
	}
	return false
}

func typedEntryKind(isDir, isSymlink, isRegular bool) string {
	switch {
	case isSymlink:
		return "symlink"
	case isDir:
		return "dir"
	case isRegular:
		return "file"
	default:
		return "other"
	}
}
