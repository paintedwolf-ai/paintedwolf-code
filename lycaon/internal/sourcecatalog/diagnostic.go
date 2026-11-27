package sourcecatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	DirAttempts     int `json:"dir_attempts"`
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
// publishes a serialized, bounded retained local JSONL report using
// cancellation-aware write admission.
type DiagnosticSampler struct {
	initOnce    sync.Once
	sem         chan struct{}
	seen        map[diagnosticDedupKey]struct{}
	retained    []DiagnosticReport
	maxSeen     int
	beforeWrite func()
}

// NewDiagnosticSampler creates a bounded deduplicating diagnostic sampler.
func NewDiagnosticSampler() *DiagnosticSampler {
	s := &DiagnosticSampler{
		sem:     make(chan struct{}, 1),
		seen:    make(map[diagnosticDedupKey]struct{}),
		maxSeen: 4096,
	}
	return s
}

func (s *DiagnosticSampler) ensureInit() {
	s.initOnce.Do(func() {
		if s.sem == nil {
			s.sem = make(chan struct{}, 1)
		}
		if s.seen == nil {
			s.seen = make(map[diagnosticDedupKey]struct{})
		}
		if s.maxSeen <= 0 {
			s.maxSeen = 4096
		}
	})
}

func (s *DiagnosticSampler) acquire(ctx context.Context) error {
	s.ensureInit()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.sem <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.sem
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *DiagnosticSampler) release() {
	<-s.sem
}

func (s *DiagnosticSampler) isSeen(ctx context.Context, key diagnosticDedupKey) (bool, error) {
	if s == nil {
		return false, ctx.Err()
	}
	if err := s.acquire(ctx); err != nil {
		return false, err
	}
	defer s.release()
	_, exists := s.seen[key]
	return exists, nil
}

// retainAndWrite serializes ownership of the retained snapshot with
// cancellation-aware admission and publishes atomically through fseffect.Replace.
// Once admitted (ctx live and admit returns true), the atomic replacement runs
// to completion before releasing the sampler so owner Drain/ReleaseTreeRoot
// observes a defined ordering. Dedup keys are committed ONLY after atomic
// publication succeeds.
func (s *DiagnosticSampler) retainAndWrite(
	ctx context.Context,
	reportPath string,
	report *DiagnosticReport,
	keys []diagnosticDedupKey,
	maxRetained int,
	admit func() bool,
) error {
	if report == nil || len(report.Disagreements) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if maxRetained <= 0 {
		maxRetained = defaultDiagnosticMaxRetainedReports
	}
	if s == nil {
		if admit != nil && !admit() {
			return context.Canceled
		}
		return writeAtomicDiagnosticReports(reportPath, []DiagnosticReport{*report})
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if err := ctx.Err(); err != nil {
		return err
	}
	if admit != nil && !admit() {
		return context.Canceled
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
	if s.beforeWrite != nil {
		s.beforeWrite()
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

// tryAcquireDiagnosticSlot enforces a global single-slot diagnostic admission
// across all roots in TreeStores so simultaneous publications cannot multiply
// filesystem and SQLite work. Lock order: c.mu -> s.mu -> c.diagMu.
func (c *TreeStores) tryAcquireDiagnosticSlot() bool {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if c.diagActive {
		return false
	}
	c.diagActive = true
	return true
}

func (c *TreeStores) releaseDiagnosticSlot() {
	c.diagMu.Lock()
	c.diagActive = false
	c.diagMu.Unlock()
}

// scheduleDiagnostic dispatches an opt-in dev diagnostic sample bound directly
// to this already-published indexStore and generation, admitted under both the
// store's owner/drain boundary (s.retired || s.pins.drained) and TreeStores'
// global single-slot admission.
func (s *indexStore) scheduleDiagnostic(published TreeStatus) {
	if s == nil || s.stores == nil || !devCatalogDiagnosticEnabled() {
		return
	}
	cfg := DiagnosticConfigFromEnv()
	if !cfg.Enabled {
		return
	}
	s.mu.Lock()
	if s.retired || s.pins.drained || s.diagDone != nil {
		s.mu.Unlock()
		return
	}
	if !s.stores.tryAcquireDiagnosticSlot() {
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
			s.stores.releaseDiagnosticSlot()
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
	retired := s.retired || s.pins.drained
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
		ctx, root.Path, cfg, policy, faults, refusedDirs,
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

	// Generation consistency assertion: verify neither retirement/drain, nor published
	// generation, nor repochange epoch, nor any sampled directory stamp moved.
	if !verifyGenerationConsistency(ctx, s, pinnedStatus, beforeEpoch, dirStamps) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
		seen, seenErr := sampler.isSeen(ctx, key)
		if seenErr != nil {
			return nil, seenErr
		}
		if seen {
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

	admitPublish := func() bool {
		s.mu.Lock()
		ok := !s.retired && !s.pins.drained &&
			s.instance == pinnedStatus.Instance &&
			s.status.Revision == pinnedStatus.Revision
		s.mu.Unlock()
		return ok
	}
	if err := sampler.retainAndWrite(ctx, cfg.ReportPath, report, candidateKeys, cfg.MaxRetainedReports, admitPublish); err != nil {
		return report, err
	}
	return report, nil
}

func verifyGenerationConsistency(
	ctx context.Context,
	s *indexStore,
	pinnedStatus TreeStatus,
	beforeEpoch repochange.Epoch,
	dirStamps map[string]DirectoryStamp,
) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	retired := s.retired || s.pins.drained
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
		if ctx.Err() != nil {
			return false
		}
		if !beforeStamp.known() {
			return false
		}
		info, statErr := osRoot.Lstat(filepath.FromSlash(dir))
		if statErr != nil {
			return false
		}
		afterStamp := directoryStampOf(info)
		if !afterStamp.known() || afterStamp != beforeStamp {
			return false
		}
	}
	return true
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
