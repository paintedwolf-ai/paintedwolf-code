package scan

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

// HeadSHAReader resolves source provenance.
type HeadSHAReader interface {
	HeadSHA(ctx context.Context, projectDir string) (string, error)
}

// EnqueueRequest is input for ScanCoordinator.Enqueue.
type EnqueueRequest struct {
	BaseSnapshotID       string
	Assessment           *AssessmentDraft
	ProjectDir           string
	Categories           []api.ScanCategory
	ScannerID            string
	Paths                []string
	DelegationID         string
	WorkflowRunID        string
	SessionID            string
	HeadSHA              string
	SourceSnapshotID     string
	AssessmentID         string
	RequiredScanners     []string
	TargetKind           api.ScanTargetKind
	DeletedPaths         []string
	ExecutionManifest    *api.ScanExecutionManifest
	ExecutionFingerprint string
	Trigger              api.ScanTrigger
	// SupersedesScanID excludes the source scan from replacement dedup.
	SupersedesScanID string
}

// ScanCoordinator enqueues and queries async code scans.
type ScanCoordinator interface {
	PublishSourceGeneration(ctx context.Context, canonicalPath string) (sourcesnapshot.Snapshot, string, error)
	// SnapshotStore is the generation store scans bind to; nil when the
	// coordinator has none.
	SnapshotStore() *sourcesnapshot.Store
	Enqueue(ctx context.Context, req EnqueueRequest) (*api.CodeScan, error)
	Get(ctx context.Context, id string) (*api.CodeScan, error)
	Summary(ctx context.Context, id string) (*api.CodeScan, error)
	ResolveID(ctx context.Context, canonicalPath, prefix string) (string, error)
	List(ctx context.Context, canonicalPaths []string, limit int) ([]api.CodeScan, error)
	ListPage(ctx context.Context, canonicalPaths []string, query PageQuery) (api.CodeScanPage, error)
	ListByWorkflowRunID(ctx context.Context, workflowRunID string) ([]api.CodeScan, error)
	ListBySessionID(ctx context.Context, sessionID string) ([]api.CodeScan, error)
	LatestForDelegation(ctx context.Context, delegationID string, categories []api.ScanCategory) (*api.CodeScan, error)
	Query(ctx context.Context, req QueryRequest) (*api.ScanQueryResponse, error)
	Compare(ctx context.Context, oldScanID, newScanID string) (*Comparison, error)
	PreviousComplete(ctx context.Context, latest api.CodeScan) (*api.CodeScan, error)
}

// CoordinatorImpl implements ScanCoordinator with dedup and ledger persistence.
type CoordinatorImpl struct {
	Store   *SQLStore
	HeadSHA HeadSHAReader
	// snapshots publishes the immutable input named by each scan.
	snapshots *sourcesnapshot.Store
	Settings  *settings.SecurityScannersStore
	Registry  CodeScannerRegistry
}

// sourceVerify is the re-read policy a scan's source publication runs under.
func (c *CoordinatorImpl) sourceVerify() sourcesnapshot.Verify {
	if c == nil || c.Settings == nil {
		return sourcesnapshot.VerifyStat
	}
	if c.Settings.Effective().SourceVerify == settings.SourceVerifyContent {
		return sourcesnapshot.VerifyContent
	}
	return sourcesnapshot.VerifyStat
}

// NewCoordinator creates a scan coordinator backed by SQLStore.
func NewCoordinator(store *SQLStore, head HeadSHAReader, snapshots *sourcesnapshot.Store) *CoordinatorImpl {
	return &CoordinatorImpl{Store: store, HeadSHA: head, snapshots: snapshots}
}

// SnapshotStore returns the source publication store shared with the runner.
func (c *CoordinatorImpl) SnapshotStore() *sourcesnapshot.Store {
	if c == nil {
		return nil
	}
	return c.snapshots
}

// PublishSourceGeneration freezes the source bytes and returns the publication
// provenance needed to derive scanner targets.
func (c *CoordinatorImpl) PublishSourceGeneration(ctx context.Context, canonicalPath string) (sourcesnapshot.Snapshot, string, error) {
	if c == nil || c.snapshots == nil {
		return sourcesnapshot.Snapshot{}, "", fmt.Errorf("scan source snapshot store not configured")
	}
	snapshot, err := c.snapshots.EnsurePath(ctx, canonicalPath, c.sourceVerify())
	if err != nil {
		return sourcesnapshot.Snapshot{}, "", err
	}
	headSHA := ""
	if c.HeadSHA != nil {
		headSHA, _ = c.HeadSHA.HeadSHA(ctx, canonicalPath)
	}
	return snapshot, strings.TrimSpace(headSHA), nil
}

// PublishPending binds a warming obligation to its source snapshot.
func (c *CoordinatorImpl) PublishPending(ctx context.Context, scanID string) error {
	if c == nil || c.Store == nil {
		return fmt.Errorf("scan coordinator not configured")
	}
	rec, err := c.Store.Get(ctx, strings.TrimSpace(scanID))
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("scan obligation %s not found", scanID)
	}
	if rec.Status != api.CodeScanStatusPending || rec.SourceSnapshotID != api.SourceSnapshotWarming {
		return nil
	}
	snapshot, snapshotErr := c.snapshots.EnsurePath(ctx, rec.CanonicalPath, c.sourceVerify())
	if snapshotErr != nil {
		return fmt.Errorf("publish scan source snapshot: %w", snapshotErr)
	}
	if c.HeadSHA != nil {
		if headSHA, headErr := c.HeadSHA.HeadSHA(ctx, rec.CanonicalPath); headErr == nil {
			_ = c.Store.bindHeadSHA(ctx, rec.ID, headSHA)
		}
	}
	_, err = c.Store.BindPublishedSourceSnapshot(ctx, rec.ID, snapshot)
	return err
}

// Enqueue creates or returns an existing scan for the dedup key.
func (c *CoordinatorImpl) Enqueue(ctx context.Context, req EnqueueRequest) (*api.CodeScan, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	canonicalPath, err := CanonicalPath(req.ProjectDir)
	if err != nil {
		return nil, err
	}
	categories, err := ResolveScanCategories(req.Categories)
	if err != nil {
		return nil, err
	}
	req.Paths = NormalizeScanPaths(req.Paths)
	req.DeletedPaths = NormalizeScanPaths(req.DeletedPaths)
	scannerID := strings.TrimSpace(req.ScannerID)
	if req.TargetKind == "" {
		if len(req.Paths) == 0 && len(req.DeletedPaths) == 0 {
			req.TargetKind = api.ScanTargetFull
		} else {
			req.TargetKind = api.ScanTargetPaths
		}
	}
	if req.ExecutionManifest == nil && c.Registry != nil {
		contract, contractErr := SelectedScannerContract(ctx, c.Registry, canonicalPath, scannerID, categories...)
		if contractErr != nil {
			return nil, contractErr
		}
		manifest, fingerprint, manifestErr := scancatalog.ExecutionManifest(contract)
		if manifestErr != nil {
			return nil, manifestErr
		}
		req.ExecutionManifest = &manifest
		req.ExecutionFingerprint = fingerprint
	}
	req.ExecutionManifest, req.ExecutionFingerprint, _, err = normalizeExecutionIdentity(
		scannerID, req.ExecutionManifest, req.ExecutionFingerprint, api.ScanFingerprintScheme,
	)
	if err != nil {
		return nil, err
	}
	reuseKey, err := scanReuseKey(api.CodeScan{
		ScannerID: scannerID, Categories: categories, TargetKind: req.TargetKind, TargetPaths: req.Paths,
		DeletedPaths: req.DeletedPaths, ExecutionFingerprint: req.ExecutionFingerprint,
		FingerprintScheme: api.ScanFingerprintScheme,
	}, req.BaseSnapshotID)
	if err != nil {
		return nil, err
	}
	headSHA := strings.TrimSpace(req.HeadSHA)
	if headSHA == "" && c.HeadSHA != nil {
		if sha, err := c.HeadSHA.HeadSHA(ctx, canonicalPath); err == nil {
			headSHA = sha
		}
	}
	trigger := req.Trigger
	if trigger == "" {
		trigger = api.ScanTriggerManual
	}
	delegationID := strings.TrimSpace(req.DelegationID)
	snapshotID := strings.TrimSpace(req.SourceSnapshotID)
	if req.AssessmentID == "" {
		req.AssessmentID = uuid.NewString()
	}
	requiredScanners := req.RequiredScanners
	if len(requiredScanners) == 0 {
		requiredScanners = []string{scannerID}
	}

	if snapshotID == "" {
		return c.enqueueWithSnapshotPublish(ctx, enqueueDraft{
			canonicalPath:        canonicalPath,
			categories:           categories,
			scannerID:            scannerID,
			headSHA:              headSHA,
			delegationID:         delegationID,
			workflowRunID:        strings.TrimSpace(req.WorkflowRunID),
			sessionID:            strings.TrimSpace(req.SessionID),
			trigger:              trigger,
			paths:                req.Paths,
			deletedPaths:         req.DeletedPaths,
			targetKind:           req.TargetKind,
			assessmentID:         strings.TrimSpace(req.AssessmentID),
			requiredScanners:     append([]string(nil), requiredScanners...),
			executionManifest:    req.ExecutionManifest,
			executionFingerprint: req.ExecutionFingerprint,
			reuseKey:             reuseKey,
			supersedes:           strings.TrimSpace(req.SupersedesScanID),
		})
	}
	if c.snapshots == nil {
		return nil, fmt.Errorf("scan source snapshot store not configured")
	}
	snapshot, err := c.snapshots.Get(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	if req.TargetKind == api.ScanTargetPaths {
		req.Paths, err = ValidateSnapshotTargets(ctx, c.snapshots, snapshot, req.Paths)
		if err != nil {
			return nil, err
		}
	}
	if err := c.ensureEnqueueAssessment(ctx, req, AssessmentDraft{
		ID: req.AssessmentID, CanonicalPath: canonicalPath, SourceSnapshotID: snapshotID,
		RequiredScanners: requiredScanners,
		Target: TargetSelection{Kind: req.TargetKind, Paths: req.Paths, DeletedPaths: req.DeletedPaths,
			CaptureQuality: string(snapshot.Quality), AdmissionMode: string(snapshot.AdmissionMode)},
		Trigger: trigger,
	}); err != nil {
		return nil, err
	}

	// Published snapshot ids define queue identity.
	queueIdentity := snapshotID
	workflowRunID := strings.TrimSpace(req.WorkflowRunID)
	sessionID := strings.TrimSpace(req.SessionID)
	if existing, err := c.reuseDedup(ctx, delegationID, canonicalPath, queueIdentity, scannerID, categories, req.Paths, reuseKey, strings.TrimSpace(req.SupersedesScanID), req.AssessmentID, workflowRunID, sessionID); err != nil || existing != nil {
		return existing, err
	}
	rec := api.CodeScan{
		ID:                   uuid.NewString(),
		CanonicalPath:        canonicalPath,
		Categories:           categories,
		ScannerID:            scannerID,
		Status:               api.CodeScanStatusPending,
		CreatedAt:            time.Now().UTC(),
		DelegationID:         delegationID,
		WorkflowRunID:        workflowRunID,
		SessionID:            sessionID,
		HeadSHA:              headSHA,
		SourceSnapshotID:     queueIdentity,
		Trigger:              trigger,
		AssessmentID:         req.AssessmentID,
		TargetKind:           req.TargetKind,
		DeletedPaths:         req.DeletedPaths,
		SourceCaptureQuality: string(snapshot.Quality),
		SourceAdmissionMode:  string(snapshot.AdmissionMode),
		ExecutionManifest:    req.ExecutionManifest,
		ExecutionFingerprint: req.ExecutionFingerprint,
		FingerprintScheme:    api.ScanFingerprintScheme,
	}
	if err := c.Store.Insert(ctx, rec, req.Paths, req.BaseSnapshotID); err != nil {
		// The unique index resolves concurrent inserts to one scan.
		if existing, dedupErr := c.reuseDedup(ctx, delegationID, canonicalPath, queueIdentity, scannerID, categories, req.Paths, reuseKey, strings.TrimSpace(req.SupersedesScanID), req.AssessmentID, workflowRunID, sessionID); dedupErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	stored, err := c.Store.Get(ctx, rec.ID)
	return projectScanContexts(stored, req.AssessmentID, workflowRunID, sessionID), err
}

type enqueueDraft struct {
	canonicalPath        string
	categories           []api.ScanCategory
	scannerID            string
	headSHA              string
	delegationID         string
	workflowRunID        string
	sessionID            string
	trigger              api.ScanTrigger
	paths                []string
	deletedPaths         []string
	targetKind           api.ScanTargetKind
	assessmentID         string
	requiredScanners     []string
	executionManifest    *api.ScanExecutionManifest
	executionFingerprint string
	reuseKey             string
	supersedes           string
}

func (c *CoordinatorImpl) enqueueWithSnapshotPublish(ctx context.Context, draft enqueueDraft) (*api.CodeScan, error) {
	if existing, err := c.findDedup(ctx, draft.delegationID, draft.canonicalPath, api.SourceSnapshotWarming, draft.scannerID, draft.categories, draft.paths, draft.reuseKey, draft.supersedes); err != nil {
		return nil, err
	} else if existing != nil {
		if err := c.ensureDraftAssessment(ctx, draft, api.SourceSnapshotWarming); err != nil {
			return nil, err
		}
		return c.bindContexts(ctx, existing, draft.assessmentID, draft.workflowRunID, draft.sessionID)
	}
	// Reuse the current publication when possible.
	if headID, err := c.snapshots.HeadIDForPath(ctx, draft.canonicalPath); err == nil && headID != "" {
		current, currentErr := c.snapshots.IsCurrent(ctx, headID)
		if currentErr != nil {
			return nil, currentErr
		}
		if current {
			if existing, err := c.findDedup(ctx, draft.delegationID, draft.canonicalPath, headID, draft.scannerID, draft.categories, draft.paths, draft.reuseKey, draft.supersedes); err != nil {
				return nil, err
			} else if existing != nil {
				if err := c.ensureDraftAssessment(ctx, draft, headID); err != nil {
					return nil, err
				}
				return c.bindContexts(ctx, existing, draft.assessmentID, draft.workflowRunID, draft.sessionID)
			}
		}
	}
	if err := c.ensureDraftAssessment(ctx, draft, api.SourceSnapshotWarming); err != nil {
		return nil, err
	}
	rec := api.CodeScan{
		ID:                   uuid.NewString(),
		CanonicalPath:        draft.canonicalPath,
		Categories:           draft.categories,
		ScannerID:            draft.scannerID,
		Status:               api.CodeScanStatusPending,
		CreatedAt:            time.Now().UTC(),
		DelegationID:         draft.delegationID,
		WorkflowRunID:        draft.workflowRunID,
		SessionID:            draft.sessionID,
		HeadSHA:              draft.headSHA,
		SourceSnapshotID:     api.SourceSnapshotWarming,
		Trigger:              draft.trigger,
		AssessmentID:         draft.assessmentID,
		TargetKind:           draft.targetKind,
		DeletedPaths:         append([]string(nil), draft.deletedPaths...),
		ExecutionManifest:    draft.executionManifest,
		ExecutionFingerprint: draft.executionFingerprint,
		FingerprintScheme:    api.ScanFingerprintScheme,
	}
	if err := c.Store.Insert(ctx, rec, draft.paths, ""); err != nil {
		// The unique index resolves concurrent inserts to one warming scan.
		if existing, dedupErr := c.reuseDedup(ctx, draft.delegationID, draft.canonicalPath, api.SourceSnapshotWarming, draft.scannerID, draft.categories, draft.paths, draft.reuseKey, draft.supersedes, draft.assessmentID, draft.workflowRunID, draft.sessionID); dedupErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	snapshot, snapshotErr := c.snapshots.EnsurePath(ctx, draft.canonicalPath, c.sourceVerify())
	if snapshotErr != nil {
		_, _ = c.Store.FinalizePendingFailure(ctx, rec.ID, FailureSourceUnavailable, fmt.Sprintf("publish scan source snapshot: %v", snapshotErr))
		return nil, fmt.Errorf("publish scan source snapshot: %w", snapshotErr)
	}
	if draft.targetKind == api.ScanTargetPaths {
		if _, targetErr := ValidateSnapshotTargets(ctx, c.snapshots, snapshot, draft.paths); targetErr != nil {
			_, _ = c.Store.FinalizePendingFailure(ctx, rec.ID, "SCAN_TARGET_SNAPSHOT_MISMATCH", targetErr.Error())
			return nil, targetErr
		}
	}
	if existing, err := c.reuseDedup(ctx, draft.delegationID, draft.canonicalPath, snapshot.ID, draft.scannerID, draft.categories, draft.paths, draft.reuseKey, draft.supersedes, draft.assessmentID, draft.workflowRunID, draft.sessionID); err != nil {
		_, _ = c.Store.FinalizePendingFailure(ctx, rec.ID, FailureCommit, err.Error())
		return nil, err
	} else if existing != nil && existing.ID != rec.ID {
		if _, bindErr := c.Store.BindPublishedSourceSnapshot(ctx, existing.ID, snapshot); bindErr != nil {
			_, _ = c.Store.FinalizePendingFailure(ctx, rec.ID, FailureCommit, bindErr.Error())
			return nil, bindErr
		}
		won, supersedeErr := c.Store.MarkPendingSuperseded(ctx, rec.ID, existing.ID)
		if supersedeErr != nil {
			return nil, supersedeErr
		}
		if !won {
			return nil, fmt.Errorf("scan %s could not be superseded by %s", rec.ID, existing.ID)
		}
		return projectScanContexts(existing, draft.assessmentID, draft.workflowRunID, draft.sessionID), nil
	}
	bound, err := c.Store.BindPublishedSourceSnapshot(ctx, rec.ID, snapshot)
	if err != nil {
		_, _ = c.Store.FinalizePendingFailure(ctx, rec.ID, FailureCommit, err.Error())
		return nil, err
	}
	return projectScanContexts(bound, draft.assessmentID, draft.workflowRunID, draft.sessionID), nil
}

func (c *CoordinatorImpl) ensureDraftAssessment(ctx context.Context, draft enqueueDraft, snapshotID string) error {
	_, err := c.Store.EnsureAssessment(ctx, AssessmentDraft{
		ID: draft.assessmentID, CanonicalPath: draft.canonicalPath, SourceSnapshotID: snapshotID,
		RequiredScanners: draft.requiredScanners,
		Target:           TargetSelection{Kind: draft.targetKind, Paths: draft.paths, DeletedPaths: draft.deletedPaths},
		Trigger:          draft.trigger,
	})
	return err
}

func (c *CoordinatorImpl) findDedup(
	ctx context.Context,
	delegationID, canonicalPath, queueIdentity, scannerID string,
	categories []api.ScanCategory,
	paths []string,
	reuseKey string,
	supersedes string,
) (*api.CodeScan, error) {
	match, err := c.matchDedup(ctx, delegationID, canonicalPath, queueIdentity, scannerID, categories, paths, reuseKey)
	if err != nil || match == nil {
		return match, err
	}
	if supersedes != "" && match.ID == supersedes {
		return nil, nil
	}
	return match, nil
}

func (c *CoordinatorImpl) matchDedup(
	ctx context.Context,
	delegationID, canonicalPath, queueIdentity, scannerID string,
	categories []api.ScanCategory,
	paths []string,
	reuseKey string,
) (*api.CodeScan, error) {
	if delegationID == "" && scannerID != "" {
		return c.Store.FindReusableByPathScanner(ctx, canonicalPath, queueIdentity, scannerID, reuseKey)
	}
	if delegationID != "" {
		return c.Store.FindInFlightForDelegationSnapshot(ctx, delegationID, queueIdentity, categories, paths)
	}
	return nil, nil
}

// Get returns a scan by id.
func (c *CoordinatorImpl) Get(ctx context.Context, id string) (*api.CodeScan, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	return c.Store.Get(ctx, id)
}

// List returns recent scans visible for canonicalPaths, newest first. limit <= 0 defaults to 20.
func (c *CoordinatorImpl) List(ctx context.Context, canonicalPaths []string, limit int) ([]api.CodeScan, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	page, err := c.Store.ListPageByCanonicalPaths(ctx, canonicalPaths, PageQuery{Limit: limit})
	return page.Scans, err
}

// ListPage returns an ordered keyset page of scan summaries.
func (c *CoordinatorImpl) ListPage(ctx context.Context, canonicalPaths []string, query PageQuery) (api.CodeScanPage, error) {
	if c == nil || c.Store == nil {
		return api.CodeScanPage{}, fmt.Errorf("scan coordinator not configured")
	}
	return c.Store.ListPageByCanonicalPaths(ctx, canonicalPaths, query)
}

// ListByWorkflowRunID returns scans bound to a workflow run (newest first).
func (c *CoordinatorImpl) ListByWorkflowRunID(ctx context.Context, workflowRunID string) ([]api.CodeScan, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	return c.Store.ListByWorkflowRunID(ctx, workflowRunID)
}

// ListBySessionID returns scans the session asked for (newest first).
func (c *CoordinatorImpl) ListBySessionID(ctx context.Context, sessionID string) ([]api.CodeScan, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	return c.Store.ListBySessionID(ctx, sessionID)
}

// LatestForDelegation returns the newest scan for delegation+categories.
func (c *CoordinatorImpl) LatestForDelegation(ctx context.Context, delegationID string, categories []api.ScanCategory) (*api.CodeScan, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	return c.Store.LatestForDelegation(ctx, delegationID, categories)
}
