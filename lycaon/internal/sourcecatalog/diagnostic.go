package sourcecatalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/repochange"
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

// DiagnosticReadCounts counts reads performed by the opt-in diagnostic itself,
// kept strictly separate from ordinary product WorkCounters.
type DiagnosticReadCounts struct {
	DirReads        int `json:"dir_reads"`
	EntryReads      int `json:"entry_reads"`
	MetadataReads   int `json:"metadata_reads"`
	CatalogRowsRead int `json:"catalog_rows_read"`
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
	Enabled            bool
	Subtree            string
	MaxDirs            int
	MaxEntries         int
	MaxDepth           int
	MaxDisagreements   int
	MaxRetainedReports int
	ReportPath         string
}

const (
	defaultDiagnosticMaxDirs            = 64
	defaultDiagnosticMaxEntries         = 1024
	defaultDiagnosticMaxDepth           = 8
	defaultDiagnosticMaxDisagreements   = 64
	defaultDiagnosticMaxRetainedReports = 32
	diagnosticSubtreeEnv                = "LYCAON_CATALOG_DIAGNOSTIC_SUBTREE"
	diagnosticDevEnv                    = "LYCAON_DEV"
)

// devCatalogDiagnosticEnabled enforces the repository's development activation
// boundary: both LYCAON_DEV and LYCAON_CATALOG_DIAGNOSTIC must be enabled.
func devCatalogDiagnosticEnabled() bool {
	return configdir.EnvTruthy(os.Getenv(diagnosticDevEnv)) &&
		debugpaths.Enabled(debugpaths.KindCatalogDiagnostic)
}

// DiagnosticConfigFromEnv builds a DiagnosticConfig gated on both LYCAON_DEV
// and LYCAON_CATALOG_DIAGNOSTIC.
func DiagnosticConfigFromEnv() DiagnosticConfig {
	enabled := devCatalogDiagnosticEnabled()
	subtree := strings.TrimSpace(os.Getenv(diagnosticSubtreeEnv))
	if subtree == "" {
		subtree = "."
	}
	return DiagnosticConfig{
		Enabled:            enabled,
		Subtree:            subtree,
		MaxDirs:            defaultDiagnosticMaxDirs,
		MaxEntries:         defaultDiagnosticMaxEntries,
		MaxDepth:           defaultDiagnosticMaxDepth,
		MaxDisagreements:   defaultDiagnosticMaxDisagreements,
		MaxRetainedReports: defaultDiagnosticMaxRetainedReports,
		ReportPath:         debugpaths.FilePath(debugpaths.KindCatalogDiagnostic),
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
	if c.MaxRetainedReports <= 0 {
		c.MaxRetainedReports = defaultDiagnosticMaxRetainedReports
	}
	return c
}

// DiagnosticReport is one bounded, deduplicated local report emitted by the
// opt-in dev catalog diagnostic. It uses typed records and identities only.
type DiagnosticReport struct {
	ProjectID            string                   `json:"project_id"`
	RootID               string                   `json:"root_id"`
	GenerationInstance   uint64                   `json:"generation_instance"`
	GenerationRevision   uint64                   `json:"generation_revision"`
	GenerationConsistent bool                     `json:"generation_consistent"`
	Subtree              string                   `json:"subtree"`
	Coverage             IndexCoverage            `json:"coverage"`
	Disagreements        []DiagnosticDisagreement `json:"disagreements"`
	DedupedCount         int                      `json:"deduped_count"`
	TotalObserved        int                      `json:"total_observed"`
	Truncated            bool                     `json:"truncated"`
	DiagnosticReads      DiagnosticReadCounts     `json:"diagnostic_reads"`
	ProductWork          WorkCounters             `json:"product_work"`
	RecordedAt           time.Time                `json:"recorded_at"`
}

type diagnosticDedupKey struct {
	projectID string
	rootID    string
	instance  uint64
	revision  uint64
	path      string
	kind      DiagnosticDisagreementKind
}

// DiagnosticSampler deduplicates disagreements across samples and atomically
// publishes a serialized, bounded retained local JSONL report.
type DiagnosticSampler struct {
	mu       sync.Mutex
	seen     map[diagnosticDedupKey]struct{}
	retained []DiagnosticReport
	maxSeen  int
}

// NewDiagnosticSampler creates a bounded deduplicating diagnostic sampler.
func NewDiagnosticSampler() *DiagnosticSampler {
	return &DiagnosticSampler{
		seen:    make(map[diagnosticDedupKey]struct{}),
		maxSeen: 4096,
	}
}

func (s *DiagnosticSampler) isSeen(key diagnosticDedupKey) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		return false
	}
	_, exists := s.seen[key]
	return exists
}

// retainAndWrite serializes ownership of the retained snapshot and publishes
// atomically through fseffect.Replace while holding the sampler lock. Dedup
// keys are committed ONLY after atomic publication succeeds, so a failed write
// never permanently suppresses the same disagreement on a subsequent sample.
func (s *DiagnosticSampler) retainAndWrite(
	reportPath string,
	report *DiagnosticReport,
	keys []diagnosticDedupKey,
	maxRetained int,
) error {
	if report == nil || len(report.Disagreements) == 0 {
		return nil
	}
	if maxRetained <= 0 {
		maxRetained = defaultDiagnosticMaxRetainedReports
	}
	if s == nil {
		return writeAtomicDiagnosticReports(reportPath, []DiagnosticReport{*report})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[diagnosticDedupKey]struct{})
	}
	filtered := make([]DiagnosticDisagreement, 0, len(report.Disagreements))
	filteredKeys := make([]diagnosticDedupKey, 0, len(keys))
	for i, d := range report.Disagreements {
		var k diagnosticDedupKey
		if i < len(keys) {
			k = keys[i]
		} else {
			k = diagnosticDedupKey{
				projectID: report.ProjectID,
				rootID:    report.RootID,
				instance:  report.GenerationInstance,
				revision:  report.GenerationRevision,
				path:      d.Path,
				kind:      d.Kind,
			}
		}
		if _, exists := s.seen[k]; exists {
			report.DedupedCount++
			continue
		}
		filtered = append(filtered, d)
		filteredKeys = append(filteredKeys, k)
	}
	report.Disagreements = filtered
	if len(filtered) == 0 {
		return nil
	}
	next := make([]DiagnosticReport, len(s.retained), len(s.retained)+1)
	copy(next, s.retained)
	next = append(next, *report)
	if len(next) > maxRetained {
		next = next[len(next)-maxRetained:]
	}
	if strings.TrimSpace(reportPath) != "" {
		if err := writeAtomicDiagnosticReports(reportPath, next); err != nil {
			return err
		}
	}
	for _, k := range filteredKeys {
		if s.maxSeen <= 0 || len(s.seen) < s.maxSeen {
			s.seen[k] = struct{}{}
		}
	}
	for _, k := range keys {
		if s.maxSeen <= 0 || len(s.seen) < s.maxSeen {
			s.seen[k] = struct{}{}
		}
	}
	s.retained = next
	return nil
}

func (c *TreeStores) diagnosticSampler() *DiagnosticSampler {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if c.diagSampler == nil {
		c.diagSampler = NewDiagnosticSampler()
	}
	return c.diagSampler
}

// scheduleDiagnostic dispatches an opt-in dev diagnostic sample bound directly
// to this already-published indexStore and generation, admitted under the store's
// owner lifecycle (canceled and joined by retireTreeStore / TreeStores.Drain /
// root removal without ever recreating a detached store).
func (s *indexStore) scheduleDiagnostic(published TreeStatus) {
	if s == nil || s.stores == nil || !devCatalogDiagnosticEnabled() {
		return
	}
	cfg := DiagnosticConfigFromEnv()
	if !cfg.Enabled {
		return
	}
	s.mu.Lock()
	if s.retired || s.diagDone != nil {
		s.mu.Unlock()
		return
	}
	diagCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan struct{})
	s.diagCancel = cancel
	s.diagDone = done
	hook := s.diagHook
	s.mu.Unlock()

	sampler := s.stores.diagnosticSampler()
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			if s.diagDone == done {
				s.diagCancel = nil
				s.diagDone = nil
			}
			s.mu.Unlock()
			close(done)
		}()
		_, _ = sampleStoreDiagnosticWithHook(diagCtx, s, published, cfg, sampler, hook)
	}()
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
// not accounted for by any declared coverage gap and only when the direct sample
// is generation-consistent with the pinned catalog generation.
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
	return c.sampleCatalogDiagnosticWithHook(ctx, projectID, root, cfg, sampler, nil)
}

func (c *TreeStores) sampleCatalogDiagnosticWithHook(
	ctx context.Context,
	projectID string,
	root Root,
	cfg DiagnosticConfig,
	sampler *DiagnosticSampler,
	afterWalk func(),
) (*DiagnosticReport, error) {
	s, err := c.indexStore(ctx, projectID, root)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	status := s.status
	s.mu.Unlock()
	return sampleStoreDiagnosticWithHook(ctx, s, status, cfg, sampler, afterWalk)
}

func sampleStoreDiagnosticWithHook(
	ctx context.Context,
	s *indexStore,
	expectedStatus TreeStatus,
	cfg DiagnosticConfig,
	sampler *DiagnosticSampler,
	afterWalk func(),
) (*DiagnosticReport, error) {
	cfg = cfg.normalized()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	retired := s.retired
	beforeStatus := s.status
	beforeStatus.Instance = s.instance
	beforeStatus.Refreshing = s.refreshingLocked()
	beforeSettledEpoch := s.epoch
	policy := s.policy
	projectID := s.projectID
	root := s.root
	s.mu.Unlock()

	if retired {
		return nil, context.Canceled
	}
	if expectedStatus.Revision != 0 && beforeStatus.Revision != expectedStatus.Revision {
		return &DiagnosticReport{
			ProjectID:            projectID,
			RootID:               root.ID,
			GenerationInstance:   beforeStatus.Instance,
			GenerationRevision:   beforeStatus.Revision,
			GenerationConsistent: false,
			Subtree:              cfg.Subtree,
			Coverage:             CoverageFromStatus(beforeStatus),
			Disagreements:        []DiagnosticDisagreement{},
			ProductWork:          s.work.snapshot(),
			RecordedAt:           time.Now().UTC(),
		}, nil
	}

	beforeEpoch := repochange.CurrentEpoch(root.Path)

	report := &DiagnosticReport{
		ProjectID:            projectID,
		RootID:               root.ID,
		GenerationInstance:   beforeStatus.Instance,
		GenerationRevision:   beforeStatus.Revision,
		GenerationConsistent: true,
		Subtree:              cfg.Subtree,
		Coverage:             CoverageFromStatus(beforeStatus),
		Disagreements:        []DiagnosticDisagreement{},
		ProductWork:          s.work.snapshot(),
		RecordedAt:           time.Now().UTC(),
	}

	// A warming, failed, or on-demand root has a declared coverage gap at root scope.
	if beforeStatus.State != StateReady || root.OnDemand || policy.rootOnDemand() {
		return report, nil
	}
	// If the repository epoch already moved past what the catalog settled, skip comparison.
	if beforeSettledEpoch != (repochange.Epoch{}) && beforeSettledEpoch != beforeEpoch {
		report.GenerationConsistent = false
		return report, nil
	}

	// Open a private read-only snapshot transaction so ordinary product
	// IndexReader.RowsRead and store.work counters are never incremented.
	db, tx, pinnedStatus, err := s.readTx(ctx, beforeStatus)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
		_ = db.Close()
	}()

	coverage, faults, refusedDirs, catalogEntries, maxCatalogPath, catalogRowsRead, catalogTruncated, err := readBoundedDiagnosticCatalogSnapshot(
		ctx, tx, pinnedStatus, cfg.Subtree, cfg.MaxEntries,
	)
	if err != nil {
		return nil, err
	}
	report.GenerationInstance = pinnedStatus.Instance
	report.GenerationRevision = pinnedStatus.Revision
	report.Coverage = coverage
	report.DiagnosticReads.CatalogRowsRead = catalogRowsRead

	// Root-level declared coverage gap suppresses all missing/stale disagreements.
	if !coverage.DiscoveryComplete || coverage.Refreshing || coverage.Error != "" {
		return report, nil
	}

	diskEntries, fullSampledDirs, dirStamps, reads, truncatedWalk, err := walkDirectSubtree(
		root.Path, cfg, policy, faults, refusedDirs,
	)
	if err != nil {
		return nil, err
	}
	reads.CatalogRowsRead = catalogRowsRead
	report.DiagnosticReads = reads
	report.ProductWork = s.work.snapshot()

	if afterWalk != nil {
		afterWalk()
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Generation consistency assertion: verify neither retirement, nor published
	// generation, nor repochange epoch, nor any sampled directory stamp moved.
	if !verifyGenerationConsistency(s, pinnedStatus, beforeEpoch, dirStamps) {
		report.GenerationConsistent = false
		return report, nil
	}

	rawDisagreements := compareCatalogAndDisk(
		catalogEntries,
		diskEntries,
		fullSampledDirs,
		faults,
		refusedDirs,
		policy,
		truncatedWalk,
		catalogTruncated,
		maxCatalogPath,
	)

	report.TotalObserved = len(rawDisagreements)
	candidateKeys := make([]diagnosticDedupKey, 0, len(rawDisagreements))
	for _, d := range rawDisagreements {
		key := diagnosticDedupKey{
			projectID: projectID,
			rootID:    root.ID,
			instance:  pinnedStatus.Instance,
			revision:  pinnedStatus.Revision,
			path:      d.Path,
			kind:      d.Kind,
		}
		if sampler.isSeen(key) {
			report.DedupedCount++
			continue
		}
		candidateKeys = append(candidateKeys, key)
		if len(report.Disagreements) >= cfg.MaxDisagreements {
			report.Truncated = true
			continue
		}
		report.Disagreements = append(report.Disagreements, d)
	}

	if err := sampler.retainAndWrite(cfg.ReportPath, report, candidateKeys, cfg.MaxRetainedReports); err != nil {
		return report, err
	}
	return report, nil
}

func verifyGenerationConsistency(
	s *indexStore,
	pinnedStatus TreeStatus,
	beforeEpoch repochange.Epoch,
	dirStamps map[string]DirectoryStamp,
) bool {
	s.mu.Lock()
	retired := s.retired
	afterStatus := s.status
	afterStatus.Instance = s.instance
	afterRefreshing := s.refreshingLocked()
	afterSettledEpoch := s.epoch
	s.mu.Unlock()

	if retired {
		return false
	}

	afterEpoch := repochange.CurrentEpoch(s.root.Path)
	if afterStatus.Instance != pinnedStatus.Instance || afterStatus.Revision != pinnedStatus.Revision {
		return false
	}
	if afterRefreshing || afterEpoch != beforeEpoch || (afterSettledEpoch != (repochange.Epoch{}) && afterSettledEpoch != beforeEpoch) {
		return false
	}
	osRoot, err := os.OpenRoot(s.root.Path)
	if err != nil {
		return false
	}
	defer func() { _ = osRoot.Close() }()
	for dir, beforeStamp := range dirStamps {
		info, statErr := osRoot.Lstat(filepath.FromSlash(dir))
		if statErr != nil {
			return false
		}
		afterStamp := directoryStampOf(info)
		if beforeStamp.known() && afterStamp != beforeStamp {
			return false
		}
	}
	return true
}

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
	rootPath string,
	cfg DiagnosticConfig,
	policy walkPolicy,
	faults map[string]struct{},
	refusedDirs map[string]string,
) (map[string]diskNodeRecord, map[string]struct{}, map[string]DirectoryStamp, DiagnosticReadCounts, bool, error) {
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
		item := queue[0]
		queue = queue[1:]

		if reads.DirReads >= cfg.MaxDirs || reads.EntryReads >= cfg.MaxEntries {
			truncated = true
			break
		}
		if hasDeclaredDirGap(item.dir, faults, refusedDirs, policy) {
			continue
		}

		f, openErr := osRoot.Open(filepath.FromSlash(item.dir))
		if openErr != nil {
			continue
		}
		dirInfo, statErr := f.Stat()
		remaining := cfg.MaxEntries - reads.EntryReads
		// Bound the underlying ReadDir syscall and working set to remaining+1 entries.
		dirEntries, readErr := f.ReadDir(remaining + 1)
		_ = f.Close()
		reads.DirReads++
		if readErr != nil && readErr != io.EOF {
			continue
		}
		dirFullyRead := len(dirEntries) <= remaining
		if !dirFullyRead {
			truncated = true
			dirEntries = dirEntries[:remaining]
		}
		if statErr == nil {
			dirStamps[item.dir] = directoryStampOf(dirInfo)
		}
		if dirFullyRead {
			fullSampledDirs[item.dir] = struct{}{}
		}

		sort.Slice(dirEntries, func(i, j int) bool { return dirEntries[i].Name() < dirEntries[j].Name() })
		for _, de := range dirEntries {
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

func writeAtomicDiagnosticReports(reportPath string, reports []DiagnosticReport) error {
	if strings.TrimSpace(reportPath) == "" || len(reports) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, r := range reports {
		encoded, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(encoded)
		buf.WriteByte('\n')
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(reportPath),
		Source:   bytes.NewReader(buf.Bytes()),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}
