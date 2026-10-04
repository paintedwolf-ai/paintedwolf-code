package sourceapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const sourceViewLifetime = 30 * time.Minute
const sourceViewCapacity = 4096
const sourceViewDescriptorBytes = 16 << 10
const sourceTreeDescriptorBytes = 1 << 20

type sourceViewCreateKey struct {
	scope             pagedview.Scope
	client, operation string
}
type sourceViewService struct {
	frameMu           sync.Mutex
	frames            *pagedview.Cache[sourceFrameKey, []byte]
	frameReservation  *pagedview.Reservation
	frameWorkers      chan struct{}
	comparisonScreens pagedview.Preparation[comparisonScreenKey, comparisonScreenResult]
	receipts          pagedview.Receipts
	snapshotDisk      *pagedview.Budget
	once              sync.Once
	mu                sync.Mutex
	registry          *pagedview.Registry[*sourceView]
	presentations     *pagedview.Registry[*sourcePresentation]
	closed            bool
	// generations counts folder changes per project, under mu.
	generations map[string]uint64
}

// errSourceViewProjectChanged reports that a project's folders changed after
// its view creation read them; the view would outlive the invalidation.
var errSourceViewProjectChanged = fmt.Errorf("%w: project folders changed during view creation", pagedview.ErrRevision)

func (service *sourceViewService) projectGeneration(project string) uint64 {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.generations[project]
}

type sourceView struct {
	secretScreenMu                   sync.Mutex
	secretScreenKey                  *comparisonScreenKey
	interests                        pagedview.ViewportInterests
	presentationMu                   sync.Mutex
	intentMu                         sync.RWMutex
	mu                               sync.Mutex
	id                               string
	scope                            pagedview.Scope
	clientID, sessionID, workspaceID string
	ctx                              context.Context
	cancel                           context.CancelFunc
	state                            string
	failure                          *wire.SourceViewFailure
	commands                         *pagedview.Commands[string]
	notifier                         *pagedview.Notifier
	// published is the last announced snapshot; only notifier delivery writes it.
	published                         [sha256.Size]byte
	tree                              *sourcetree.View
	treeInitialized                   bool
	treePrepared                      bool
	reviewPreparing                   bool
	reviewRunning                     bool
	reviewDirty                       bool
	reviewGeneration                  string
	reviewCancel                      context.CancelFunc
	filtered                          *sourcetree.Filtered
	filterCancel                      context.CancelFunc
	filterGeneration                  string
	filterRunning, filterDirty        bool
	treeIntent                        wire.SourceTreeIntent
	treeNavigationBasis               string
	roots                             []wire.ProjectRoot
	comparisonIntent                  wire.SourceComparisonIntent
	comparisonSource                  wire.SourceComparisonSelector
	chatSource                        *wire.ChatComparisonSource
	comparison                        *sourcecomparison.Document
	current                           *currentSourceSnapshot
	comparisonRelease                 func()
	comparisonBefore, comparisonAfter wire.SourceComparisonSide
	projection                        *sourceViewProjection
	comparisonBudget                  *pagedview.Budget
	descriptorBytes                   int64
	trimDescriptor                    func(int64) error
	projectionRevision                string
	details                           *wire.SourceComparisonDetails
	expires                           time.Time
}

//nolint:contextcheck,nolintlint // The registry and its sweeper follow the server lifetime.
func (s *Handler) sourceViewRegistry() *sourceViewService {
	service := &s.sourceViews
	service.once.Do(func() {
		service.mu.Lock()
		service.snapshotDisk = pagedview.NewBudget(2 << 30)
		service.frameWorkers = make(chan struct{}, 2)
		var frameBytes int64
		service.frameReservation, _ = s.sourceReaders.Budget().Reserve(8 << 20)
		if service.frameReservation != nil {
			frameBytes = 8 << 20
		}
		service.frames = pagedview.NewCache[sourceFrameKey, []byte](128, frameBytes)
		service.registry = pagedview.NewRegistry[*sourceView](s.sourceReaders.Budget(), sourceViewCapacity, sourceViewLifetime)
		service.presentations = pagedview.NewLeaseRegistry[*sourcePresentation](s.sourceReaders.Budget(), sourceViewCapacity*2, sourceViewLifetime)
		service.mu.Unlock()
		s.background.GoService(func(ctx context.Context) {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			defer service.close()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					service.sweep()
					s.sourceReaders.Sweep()
				}
			}
		})
	})
	return service
}

// create retains the accepted descriptor before work begins. The returned pin
// lasts until the caller transfers it to preparation or releases it.
func (service *sourceViewService) create(ctx context.Context, key sourceViewCreateKey, canonical []byte, generation uint64, build func() *sourceView) (*sourceView, func(), bool, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return nil, nil, false, pagedview.ErrExpired
	}
	if service.generations[key.scope.Project] != generation {
		return nil, nil, false, errSourceViewProjectChanged
	}
	namespaceBytes, err := json.Marshal(struct {
		Scope  pagedview.Scope
		Client string
	}{key.scope, key.client})
	if err != nil {
		return nil, nil, false, err
	}
	namespaceDigest := sha256.Sum256(namespaceBytes)
	namespace := "create:" + hex.EncodeToString(namespaceDigest[:])
	digest := sha256.Sum256(canonical)
	saved, found, err := service.receipts.Lookup(ctx, namespace, key.operation)
	if err != nil {
		return nil, nil, false, err
	}
	if found {
		if !bytes.Equal(saved.Digest, digest[:]) {
			return nil, nil, false, pagedview.ErrOperationConflict
		}
		view, release, err := service.registry.Acquire(key.scope, string(saved.Value))
		return view, release, false, err
	}
	if err := service.receipts.Reserve(ctx, namespace, key.operation, digest[:]); err != nil {
		return nil, nil, false, err
	}
	view := build()
	view.descriptorBytes = sourceViewDescriptorBytes + 2*int64(len(canonical))
	if view.tree != nil {
		view.descriptorBytes += sourceTreeDescriptorBytes
	}
	id, release, err := service.registry.PutPinned(key.scope, view, view.descriptorBytes, func(view *sourceView) {
		view.close()
		if err := service.receipts.Release("presentation:" + view.id); err != nil {
			slog.WarnContext(view.ctx, "source presentation receipt cleanup failed", "error", err)
		}
	})
	if err != nil {
		view.close()
		if abortErr := service.receipts.Abort(ctx, namespace, key.operation); abortErr != nil {
			err = abortErr
		}
		return nil, nil, false, err
	}
	view.id = id
	view.trimDescriptor = func(bytes int64) error {
		view.descriptorBytes = max(sourceViewDescriptorBytes, view.descriptorBytes-bytes)
		return service.registry.Resize(key.scope, id, view.descriptorBytes)
	}
	if err := service.receipts.Complete(ctx, namespace, key.operation, []byte(id)); err != nil {
		view.cancel()
		service.registry.Release(key.scope, id)
		release()
		return nil, nil, false, err
	}
	return view, release, true, nil
}

func (service *sourceViewService) sweep() {
	service.presentations.Sweep()
	service.registry.Sweep()
}

func (service *sourceViewService) close() {
	service.mu.Lock()
	service.closed = true
	service.mu.Unlock()
	service.presentations.Close()
	service.registry.Close()
	service.comparisonScreens.Close()
	service.receipts.Close()
	if service.frameReservation != nil {
		service.frameReservation.Close()
	}
}

// InvalidateProjectSourceViews ends every view of a project whose folders changed.
func (s *Handler) InvalidateProjectSourceViews(projectID string) {
	service := s.sourceViewRegistry()
	service.mu.Lock()
	if service.generations == nil {
		service.generations = make(map[string]uint64)
	}
	service.generations[projectID]++
	service.mu.Unlock()
	s.invalidateSourceViews(func(view *sourceView) bool { return view.scope.Project == projectID })
}

// ReleaseChatSourceViews ends every view addressed by a deleted chat.
func (s *Handler) ReleaseChatSourceViews(sessionID string) {
	if sessionID == "" {
		return
	}
	s.invalidateSourceViews(func(view *sourceView) bool { return view.sessionID == sessionID })
}

// invalidateSourceViews releases matching views and their presentations. Later
// requests for those handles answer expired.
func (s *Handler) invalidateSourceViews(matches func(*sourceView) bool) {
	service := &s.sourceViews
	service.mu.Lock()
	registry, presentations := service.registry, service.presentations
	service.mu.Unlock()
	if registry == nil {
		return
	}
	ended := make(map[string]bool)
	registry.Invalidate(func(view *sourceView) bool {
		if !matches(view) {
			return false
		}
		ended[view.id] = true
		return true
	}, func(view *sourceView) { s.publishSourceViewInvalidated(view); view.cancel() })
	presentations.ReleaseWhere(func(p *sourcePresentation) bool { return ended[p.read.id] })
}

func (view *sourceView) close() {
	view.cancel()
	view.interests.Close()
	view.commands.Close()
	if view.tree != nil {
		view.tree.Close()
	}
	if view.filtered != nil {
		view.filtered.Close()
	}
	if view.notifier != nil {
		view.notifier.Close()
	}
	view.projection.release()
	if view.comparisonRelease != nil {
		view.comparisonRelease()
	}
}

func (view *sourceView) touch() {
	view.mu.Lock()
	view.expires = time.Now().Add(sourceViewLifetime)
	view.mu.Unlock()
}

func sourceViewCanonical(value any) ([]byte, error) { return json.Marshal(value) }
