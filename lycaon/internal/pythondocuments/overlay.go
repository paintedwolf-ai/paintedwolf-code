package pythondocuments

import (
	"context"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

const maxSymlinkHops = 40

type nodeKind uint8

const (
	nodeFile nodeKind = iota + 1
	nodeDir
	nodeSymlink
	nodeTombstone
)

type overlayNode struct {
	mu               sync.Mutex
	kind             nodeKind
	target           pythonruntime.CanonicalTarget
	data             []byte
	mode             fs.FileMode
	modTime          time.Time
	inode            uint64
	linkTarget       string
	createdInOverlay bool
	contentModified  bool
	originTarget     *pythonruntime.CanonicalTarget
	mutationSeq      uint64
	parentOpID       string
	writeOriginFrame *pythonruntime.DeclaredFrame
	flushCloseFrame  *pythonruntime.DeclaredFrame
}

// OverlayConfig configures an OverlayAdapter for one Portable Python execution.
// Reached-operation admission is owned exclusively by PY02's WorkspaceSysFS,
// which passes its operation ID via WithParentOperationID(ctx, opID).
type OverlayConfig struct {
	ExecutionID string
	Store       DocumentStore
	Roots       []RootSpec
	CWD         string
	Evidence    EvidenceSink
	Now         func() time.Time
	// ReadOnlyFiles optionally populates immutable NamespaceRuntime and
	// NamespacePackages entries keyed by CanonicalKey.
	ReadOnlyFiles map[CanonicalKey][]byte
}

// OverlayAdapter implements pythonruntime.WorkspaceAdapter with per-program
// overlay visibility, canonical earliest-base revision pinning, multi-root
// path resolution, scratch isolation, and clean-success host settlement.
type OverlayAdapter struct {
	executionID string
	store       DocumentStore
	evidence    EvidenceSink
	now         func() time.Time

	defaultRoot RootSpec
	rootsByID   map[string]RootSpec
	rootsByLbl  map[string]RootSpec
	cwd         string
	roFiles     map[CanonicalKey][]byte

	pins *RevisionPinTable

	mu        sync.Mutex
	overlay   map[CanonicalKey]*overlayNode
	scratch   map[CanonicalKey]*overlayNode
	nextSeq   uint64
	nextOpID  uint64
	nextInode uint64
}

var _ pythonruntime.WorkspaceAdapter = (*OverlayAdapter)(nil)

// NewOverlayAdapter constructs a fresh per-program OverlayAdapter.
func NewOverlayAdapter(cfg OverlayConfig) *OverlayAdapter {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	rootsByID := make(map[string]RootSpec, len(cfg.Roots))
	rootsByLbl := make(map[string]RootSpec, len(cfg.Roots))
	var defaultRoot RootSpec
	hasDefault := false
	for _, r := range cfg.Roots {
		if r.ID == "" {
			r.ID = "default"
		}
		if r.Label == "" {
			r.Label = r.ID
		}
		rootsByID[r.ID] = r
		rootsByLbl[r.Label] = r
		if r.Default || !hasDefault {
			defaultRoot = r
			hasDefault = true
		}
	}
	if !hasDefault {
		defaultRoot = RootSpec{ID: "default", Label: "workspace", Default: true}
		rootsByID[defaultRoot.ID] = defaultRoot
		rootsByLbl[defaultRoot.Label] = defaultRoot
	}
	roFiles := make(map[CanonicalKey][]byte, len(cfg.ReadOnlyFiles))
	for k, v := range cfg.ReadOnlyFiles {
		roFiles[k] = append([]byte(nil), v...)
	}
	return &OverlayAdapter{
		executionID: cfg.ExecutionID,
		store:       cfg.Store,
		evidence:    cfg.Evidence,
		now:         now,
		defaultRoot: defaultRoot,
		rootsByID:   rootsByID,
		rootsByLbl:  rootsByLbl,
		cwd:         cleanRelPath(cfg.CWD),
		roFiles:     roFiles,
		pins:        NewRevisionPinTable(now),
		overlay:     make(map[CanonicalKey]*overlayNode),
		scratch:     make(map[CanonicalKey]*overlayNode),
		nextInode:   100_000,
	}
}

// Pins returns the adapter's canonical earliest-base revision pin table.
func (a *OverlayAdapter) Pins() *RevisionPinTable {
	return a.pins
}

func (a *OverlayAdapter) allocSeqLocked() uint64 {
	a.nextSeq++
	return a.nextSeq
}

func (a *OverlayAdapter) allocOpIDLocked() string {
	a.nextOpID++
	if a.executionID != "" {
		return fmt.Sprintf("%s-op-%d", a.executionID, a.nextOpID)
	}
	return fmt.Sprintf("op-%d", a.nextOpID)
}

func (a *OverlayAdapter) allocInodeLocked() uint64 {
	a.nextInode++
	return a.nextInode
}

func (a *OverlayAdapter) recordOverlayEventLocked(parentOpID, op string, target pythonruntime.CanonicalTarget, prev *pythonruntime.CanonicalTarget, bytesAffected int64, writeOrigin, flushClose *pythonruntime.DeclaredFrame) {
	if a.evidence == nil {
		return
	}
	seq := a.allocSeqLocked()
	opID := a.allocOpIDLocked()
	var prevCopy *pythonruntime.CanonicalTarget
	if prev != nil {
		c := *prev
		prevCopy = &c
	}
	a.evidence.RecordOverlayEvent(OverlayEventRecord{
		ExecutionID:       a.executionID,
		OperationID:       opID,
		ParentOperationID: parentOpID,
		Sequence:          seq,
		Operation:         op,
		Target:            target,
		PreviousTarget:    prevCopy,
		BytesAffected:     bytesAffected,
		WriteOriginFrame:  writeOrigin,
		FlushCloseFrame:   flushClose,
		RecordedAt:        a.now().UTC(),
	})
}

// recordDocumentReadWitness records the revision witness at Open with
// BytesDeliveredToGuest = 0; subsequent GuestFile.Read calls accumulate actual
// delivered bytes and ranges against the returned operation ID.
func (a *OverlayAdapter) recordDocumentReadWitnessLocked(parentOpID string, snap DocumentSnapshot, frame *pythonruntime.DeclaredFrame) string {
	if a.evidence == nil {
		return ""
	}
	opID := a.allocOpIDLocked()
	a.evidence.RecordDocumentRead(DocumentReadRecord{
		ExecutionID:           a.executionID,
		OperationID:           opID,
		ParentOperationID:     parentOpID,
		Target:                snap.Target,
		Revision:              snap.Revision,
		BytesDeliveredToGuest: 0,
		UnsavedDraft:          snap.Revision.UnsavedDraft,
		WriteOriginFrame:      frame,
		RecordedAt:            a.now().UTC(),
	})
	return opID
}

func (a *OverlayAdapter) pinHostStateLocked(ctx context.Context, target pythonruntime.CanonicalTarget, origin PinOrigin) (DocumentSnapshot, pythonruntime.Errno) {
	if target.Namespace != pythonruntime.NamespaceWorkspace {
		return DocumentSnapshot{Target: target}, pythonruntime.ErrnoENOENT
	}
	if a.store == nil {
		rev := DocumentRevision{RootID: target.RootID, Branch: target.Branch, Absent: true}
		a.pins.PinEarliest(target, rev, origin)
		return DocumentSnapshot{Target: target, Revision: rev}, pythonruntime.ErrnoENOENT
	}
	snap, errno := a.store.ReadDocument(ctx, target)
	if errno == pythonruntime.ErrnoENOENT {
		rev := snap.Revision
		rev.Absent = true
		if rev.RootID == "" {
			rev.RootID = target.RootID
		}
		if rev.Branch == "" {
			rev.Branch = target.Branch
		}
		a.pins.PinEarliest(target, rev, origin)
		return DocumentSnapshot{Target: target, Revision: rev}, pythonruntime.ErrnoENOENT
	}
	if errno == pythonruntime.ErrnoSuccess {
		a.pins.PinEarliest(snap.Target, snap.Revision, origin)
	}
	return snap, errno
}

func (a *OverlayAdapter) existedInHostLocked(ctx context.Context, target pythonruntime.CanonicalTarget) bool {
	if target.Namespace != pythonruntime.NamespaceWorkspace {
		return false
	}
	if pin, ok := a.pins.Get(target); ok {
		return !pin.BaseRevision.Absent
	}
	if a.store == nil {
		return false
	}
	_, errno := a.store.StatDocument(ctx, target)
	return errno == pythonruntime.ErrnoSuccess
}
