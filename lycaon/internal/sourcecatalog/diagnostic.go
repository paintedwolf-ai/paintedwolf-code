package sourcecatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

// WorkCounters tracks ordinary product catalog work independently of any
// opt-in diagnostic sampling.
type WorkCounters struct {
	DirReads         int64 `json:"dir_reads"`
	MetadataReads    int64 `json:"metadata_reads"`
	PublishedBatches int64 `json:"published_batches"`
	RowsRead         int64 `json:"rows_read"`
}

type atomicWorkCounters struct {
	dirReads         atomic.Int64
	metadataReads    atomic.Int64
	publishedBatches atomic.Int64
	rowsRead         atomic.Int64
}

func (c *atomicWorkCounters) snapshot() WorkCounters {
	if c == nil {
		return WorkCounters{}
	}
	return WorkCounters{
		DirReads:         c.dirReads.Load(),
		MetadataReads:    c.metadataReads.Load(),
		PublishedBatches: c.publishedBatches.Load(),
		RowsRead:         c.rowsRead.Load(),
	}
}

// DiagnosticReadCounts counts filesystem reads performed by the opt-in
// diagnostic itself, kept strictly separate from ordinary product WorkCounters.
type DiagnosticReadCounts struct {
	DirReads      int `json:"dir_reads"`
	EntryReads    int `json:"entry_reads"`
	MetadataReads int `json:"metadata_reads"`
}

// DiagnosticDisagreementKind identifies a typed mismatch between the catalog
// and a direct filesystem listing that no declared coverage gap accounts for.
type DiagnosticDisagreementKind string

const (
	DisagreementMissingInCatalog DiagnosticDisagreementKind = "missing_in_catalog"
	DisagreementExtraInCatalog   DiagnosticDisagreementKind = "extra_in_catalog"
	DisagreementKindMismatch     DiagnosticDisagreementKind = "kind_mismatch"
)

// DiagnosticDisagreement is one typed catalog-vs-filesystem disagreement.
type DiagnosticDisagreement struct {
	Path        string                     `json:"path"`
	Kind        DiagnosticDisagreementKind `json:"kind"`
	CatalogKind string                     `json:"catalog_kind,omitempty"`
	DiskKind    string                     `json:"disk_kind,omitempty"`
}

// DiagnosticConfig bounds one opt-in dev diagnostic subtree sample.
type DiagnosticConfig struct {
	Enabled          bool
	Subtree          string
	MaxDirs          int
	MaxEntries       int
	MaxDepth         int
	MaxDisagreements int
	ReportPath       string
}

const (
	defaultDiagnosticMaxDirs          = 64
	defaultDiagnosticMaxEntries       = 1024
	defaultDiagnosticMaxDepth         = 8
	defaultDiagnosticMaxDisagreements = 64
)

// DiagnosticConfigFromEnv builds a DiagnosticConfig gated on LYCAON_CATALOG_DIAGNOSTIC.
func DiagnosticConfigFromEnv() DiagnosticConfig {
	enabled := debugpaths.Enabled(debugpaths.KindCatalogDiagnostic)
	return DiagnosticConfig{
		Enabled:          enabled,
		Subtree:          ".",
		MaxDirs:          defaultDiagnosticMaxDirs,
		MaxEntries:       defaultDiagnosticMaxEntries,
		MaxDepth:         defaultDiagnosticMaxDepth,
		MaxDisagreements: defaultDiagnosticMaxDisagreements,
		ReportPath:       debugpaths.FilePath(debugpaths.KindCatalogDiagnostic),
	}
}

func (c DiagnosticConfig) normalized() DiagnosticConfig {
	if c.Subtree == "" {
		c.Subtree = "."
	} else {
		c.Subtree = normalizeDir(c.Subtree)
	}
	if c.MaxDirs <= 0 {
		c.MaxDirs = defaultDiagnosticMaxDirs
	}
	if c.MaxEntries <= 0 {
		c.MaxEntries = defaultDiagnosticMaxEntries
	}
	if c.MaxDepth <= 0 {
		c.MaxDepth = defaultDiagnosticMaxDepth
	}
	if c.MaxDisagreements <= 0 {
		c.MaxDisagreements = defaultDiagnosticMaxDisagreements
	}
	return c
}

// DiagnosticReport is one bounded, deduplicated local report emitted by the
// opt-in dev catalog diagnostic. It uses typed records and identities only.
type DiagnosticReport struct {
	ProjectID          string                   `json:"project_id"`
	RootID             string                   `json:"root_id"`
	GenerationInstance uint64                   `json:"generation_instance"`
	GenerationRevision uint64                   `json:"generation_revision"`
	Subtree            string                   `json:"subtree"`
	Coverage           IndexCoverage            `json:"coverage"`
	Disagreements      []DiagnosticDisagreement `json:"disagreements"`
	DedupedCount       int                      `json:"deduped_count"`
	TotalObserved      int                      `json:"total_observed"`
	Truncated          bool                     `json:"truncated"`
	DiagnosticReads    DiagnosticReadCounts     `json:"diagnostic_reads"`
	ProductWork        WorkCounters             `json:"product_work"`
	RecordedAt         time.Time                `json:"recorded_at"`
}

type diagnosticDedupKey struct {
	projectID string
	rootID    string
	instance  uint64
	revision  uint64
	path      string
	kind      DiagnosticDisagreementKind
}

// DiagnosticSampler deduplicates disagreements across samples and writes
// bounded local JSONL reports.
type DiagnosticSampler struct {
	mu      sync.Mutex
	seen    map[diagnosticDedupKey]struct{}
	maxSeen int
}

// NewDiagnosticSampler creates a bounded deduplicating diagnostic sampler.
func NewDiagnosticSampler() *DiagnosticSampler {
	return &DiagnosticSampler{
		seen:    make(map[diagnosticDedupKey]struct{}),
		maxSeen: 4096,
	}
}

func (s *DiagnosticSampler) recordUnique(key diagnosticDedupKey) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[diagnosticDedupKey]struct{})
	}
	if _, exists := s.seen[key]; exists {
		return false
	}
	if s.maxSeen > 0 && len(s.seen) >= s.maxSeen {
		clear(s.seen)
	}
	s.seen[key] = struct{}{}
	return true
}

type catalogNodeRecord struct {
	path      string
	kind      string
	refused   string
	indexed   bool
	isDir     bool
	isSymlink bool
}

type diskNodeRecord struct {
	path  string
	kind  string
	isDir bool
}

// ProductWorkCounters returns a snapshot of ordinary product work counters for
// a root's index store without performing any I/O or modifying state.
func (c *TreeStores) ProductWorkCounters(ctx context.Context, projectID string, root Root) (WorkCounters, error) {
	s, err := c.indexStore(ctx, projectID, root)
	if err != nil {
		return WorkCounters{}, err
	}
	return s.work.snapshot(), nil
}

// SampleCatalogDiagnostic compares a bounded subtree of the published catalog
// against an independent direct filesystem listing, reporting only disagreements
// not accounted for by any declared coverage gap.
//
// When cfg.Enabled is false, it returns immediately with zero work.
func (c *TreeStores) SampleCatalogDiagnostic(
	ctx context.Context,
	projectID string,
	root Root,
	cfg DiagnosticConfig,
	sampler *DiagnosticSampler,
) (*DiagnosticReport, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	cfg = cfg.normalized()

	s, err := c.indexStore(ctx, projectID, root)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	status := s.status
	status.Instance = s.instance
	status.Refreshing = s.refreshingLocked()
	policy := s.policy
	s.mu.Unlock()

	report := &DiagnosticReport{
		ProjectID:          projectID,
		RootID:             s.root.ID,
		GenerationInstance: status.Instance,
		GenerationRevision: status.Revision,
		Subtree:            cfg.Subtree,
		Coverage:           CoverageFromStatus(status),
		Disagreements:      []DiagnosticDisagreement{},
		ProductWork:        s.work.snapshot(),
		RecordedAt:         time.Now().UTC(),
	}

	// A warming, failed, or on-demand root has a declared coverage gap at root scope.
	if status.State != StateReady || s.root.OnDemand || policy.rootOnDemand() {
		return report, writeDiagnosticReport(cfg.ReportPath, report)
	}

	// Open a private read-only snapshot transaction so ordinary product
	// IndexReader.RowsRead and store.work counters are never incremented.
	db, tx, pinnedStatus, err := s.readTx(ctx, status)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
		_ = db.Close()
	}()

	coverage, faults, refusedDirs, catalogEntries, err := readDiagnosticCatalogSnapshot(ctx, tx, pinnedStatus, cfg.Subtree)
	if err != nil {
		return nil, err
	}
	report.GenerationInstance = pinnedStatus.Instance
	report.GenerationRevision = pinnedStatus.Revision
	report.Coverage = coverage

	// Root-level declared coverage gap suppresses all missing/stale disagreements.
	if !coverage.DiscoveryComplete || coverage.Refreshing || coverage.Error != "" {
		return report, writeDiagnosticReport(cfg.ReportPath, report)
	}

	diskEntries, sampledDirs, reads, truncatedWalk, err := walkDirectSubtree(s.root.Path, cfg, policy, faults, refusedDirs)
	if err != nil {
		return nil, err
	}
	report.DiagnosticReads = reads
	report.ProductWork = s.work.snapshot()

	rawDisagreements := compareCatalogAndDisk(
		catalogEntries,
		diskEntries,
		sampledDirs,
		faults,
		refusedDirs,
		policy,
		truncatedWalk,
	)

	report.TotalObserved = len(rawDisagreements)
	for _, d := range rawDisagreements {
		key := diagnosticDedupKey{
			projectID: projectID,
			rootID:    s.root.ID,
			instance:  pinnedStatus.Instance,
			revision:  pinnedStatus.Revision,
			path:      d.Path,
			kind:      d.Kind,
		}
		if !sampler.recordUnique(key) {
			report.DedupedCount++
			continue
		}
		if len(report.Disagreements) >= cfg.MaxDisagreements {
			report.Truncated = true
			continue
		}
		report.Disagreements = append(report.Disagreements, d)
	}

	return report, writeDiagnosticReport(cfg.ReportPath, report)
}

func readDiagnosticCatalogSnapshot(
	ctx context.Context,
	tx *sql.Tx,
	status TreeStatus,
	subtree string,
) (IndexCoverage, map[string]struct{}, map[string]string, map[string]catalogNodeRecord, error) {
	coverage := CoverageFromStatus(status)
	err := tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM nodes WHERE refused<>'' AND refused<>'lazy'),
		(SELECT count(*) FROM faults)`).Scan(&coverage.BoundedDirectories, &coverage.FailedDirectories)
	if err != nil {
		return IndexCoverage{}, nil, nil, nil, err
	}

	faults := make(map[string]struct{})
	faultRows, err := tx.QueryContext(ctx, "SELECT path FROM faults")
	if err != nil {
		return IndexCoverage{}, nil, nil, nil, err
	}
	for faultRows.Next() {
		var p string
		if err := faultRows.Scan(&p); err != nil {
			_ = faultRows.Close()
			return IndexCoverage{}, nil, nil, nil, err
		}
		faults[p] = struct{}{}
	}
	if err := faultRows.Close(); err != nil {
		return IndexCoverage{}, nil, nil, nil, err
	}

	refusedDirs := make(map[string]string)
	entries := make(map[string]catalogNodeRecord)
	nodeRows, err := tx.QueryContext(ctx, "SELECT path, is_dir, symlink, regular, refused, indexed FROM nodes")
	if err != nil {
		return IndexCoverage{}, nil, nil, nil, err
	}
	for nodeRows.Next() {
		var p, refused string
		var isDir, isSymlink, isRegular, indexed bool
		if err := nodeRows.Scan(&p, &isDir, &isSymlink, &isRegular, &refused, &indexed); err != nil {
			_ = nodeRows.Close()
			return IndexCoverage{}, nil, nil, nil, err
		}
		if refused != "" {
			refusedDirs[p] = refused
		}
		if p == "." || !indexed {
			continue
		}
		if !pathInSubtree(p, subtree) {
			continue
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
		return IndexCoverage{}, nil, nil, nil, err
	}

	return coverage, faults, refusedDirs, entries, nil
}

func walkDirectSubtree(
	rootPath string,
	cfg DiagnosticConfig,
	policy walkPolicy,
	faults map[string]struct{},
	refusedDirs map[string]string,
) (map[string]diskNodeRecord, map[string]struct{}, DiagnosticReadCounts, bool, error) {
	osRoot, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, DiagnosticReadCounts{}, false, err
	}
	defer func() { _ = osRoot.Close() }()

	type queueItem struct {
		dir   string
		depth int
	}

	diskEntries := make(map[string]diskNodeRecord)
	sampledDirs := make(map[string]struct{})
	var reads DiagnosticReadCounts
	truncated := false

	queue := []queueItem{{dir: cfg.Subtree, depth: 0}}
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if reads.DirReads >= cfg.MaxDirs {
			truncated = true
			break
		}
		if hasDeclaredDirGap(item.dir, faults, refusedDirs, policy) {
			continue
		}

		f, openErr := osRoot.Open(filepath.FromSlash(item.dir))
		if openErr != nil {
			// If a directory became unreadable after publication, do not fail the whole diagnostic.
			continue
		}
		dirEntries, readErr := f.ReadDir(-1)
		_ = f.Close()
		reads.DirReads++
		if readErr != nil {
			continue
		}
		sampledDirs[item.dir] = struct{}{}

		sort.Slice(dirEntries, func(i, j int) bool { return dirEntries[i].Name() < dirEntries[j].Name() })
		for _, de := range dirEntries {
			if reads.EntryReads >= cfg.MaxEntries {
				truncated = true
				break
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
				info, statErr := osRoot.Lstat(filepath.FromSlash(rel))
				reads.MetadataReads++
				if statErr != nil {
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

	return diskEntries, sampledDirs, reads, truncated, nil
}

func compareCatalogAndDisk(
	catalogEntries map[string]catalogNodeRecord,
	diskEntries map[string]diskNodeRecord,
	sampledDirs map[string]struct{},
	faults map[string]struct{},
	refusedDirs map[string]string,
	policy walkPolicy,
	truncatedWalk bool,
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
		if _, sampled := sampledDirs[parent]; !sampled {
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

func pathInSubtree(rel, subtree string) bool {
	if subtree == "." || subtree == "" {
		return true
	}
	return rel == subtree || strings.HasPrefix(rel, subtree+"/")
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

func writeDiagnosticReport(reportPath string, report *DiagnosticReport) error {
	if strings.TrimSpace(reportPath) == "" || report == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(reportPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(encoded, '\n'))
	closeErr := f.Close()
	return errors.Join(writeErr, closeErr)
}
