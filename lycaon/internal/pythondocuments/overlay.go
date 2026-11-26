package pythondocuments

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
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
	writeOriginFrame *pythonruntime.DeclaredFrame
	flushCloseFrame  *pythonruntime.DeclaredFrame
}

// OverlayConfig configures an OverlayAdapter for one Portable Python execution.
type OverlayConfig struct {
	ExecutionID string
	Store       DocumentStore
	Roots       []RootSpec
	CWD         string
	Observer    pythonruntime.OperationObserver
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
	observer    pythonruntime.OperationObserver
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
		observer:    cfg.Observer,
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

func (a *OverlayAdapter) resolvePath(raw string) (pythonruntime.CanonicalTarget, RootSpec, pythonruntime.Errno) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoENOENT
	}
	if raw == "/dev/null" {
		return pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceNullDevice}, RootSpec{}, pythonruntime.ErrnoSuccess
	}
	if raw == "/scratch" || strings.HasPrefix(raw, "/scratch/") {
		rel := strings.TrimPrefix(raw, "/scratch")
		rel = strings.TrimPrefix(rel, "/")
		cleaned, ok := cleanConfinedRel(rel)
		if !ok {
			return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
		}
		return pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceScratch,
			RootID:    "scratch",
			RelPath:   cleaned,
		}, RootSpec{ID: "scratch", Label: "scratch"}, pythonruntime.ErrnoSuccess
	}
	if raw == "/runtime" || strings.HasPrefix(raw, "/runtime/") {
		rel := strings.TrimPrefix(raw, "/runtime")
		rel = strings.TrimPrefix(rel, "/")
		cleaned, ok := cleanConfinedRel(rel)
		if !ok {
			return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
		}
		return pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceRuntime,
			RootID:    "runtime",
			RelPath:   cleaned,
		}, RootSpec{ID: "runtime", Label: "runtime", ReadOnly: true}, pythonruntime.ErrnoSuccess
	}
	if raw == "/packages" || strings.HasPrefix(raw, "/packages/") {
		rel := strings.TrimPrefix(raw, "/packages")
		rel = strings.TrimPrefix(rel, "/")
		cleaned, ok := cleanConfinedRel(rel)
		if !ok {
			return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
		}
		return pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespacePackages,
			RootID:    "packages",
			RelPath:   cleaned,
		}, RootSpec{ID: "packages", Label: "packages", ReadOnly: true}, pythonruntime.ErrnoSuccess
	}

	// Attached root addressing: @label/sub or /@label/sub
	trimmed := strings.TrimPrefix(raw, "/")
	if strings.HasPrefix(trimmed, "@") {
		rest := strings.TrimPrefix(trimmed, "@")
		label, sub, _ := strings.Cut(rest, "/")
		root, ok := a.rootsByLbl[label]
		if !ok {
			return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoENOENT
		}
		cleaned, ok := cleanConfinedRel(sub)
		if !ok {
			return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
		}
		return pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    root.ID,
			Branch:    root.Branch,
			RelPath:   cleaned,
		}, root, pythonruntime.ErrnoSuccess
	}

	// Default workspace root via /workspace/... or relative path against CWD.
	if raw == "/workspace" || strings.HasPrefix(raw, "/workspace/") {
		rel := strings.TrimPrefix(raw, "/workspace")
		rel = strings.TrimPrefix(rel, "/")
		cleaned, ok := cleanConfinedRel(rel)
		if !ok {
			return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
		}
		return pythonruntime.CanonicalTarget{
			Namespace: pythonruntime.NamespaceWorkspace,
			RootID:    a.defaultRoot.ID,
			Branch:    a.defaultRoot.Branch,
			RelPath:   cleaned,
		}, a.defaultRoot, pythonruntime.ErrnoSuccess
	}

	if strings.HasPrefix(raw, "/") {
		return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
	}

	joined := raw
	if a.cwd != "" {
		joined = a.cwd + "/" + raw
	}
	cleaned, ok := cleanConfinedRel(joined)
	if !ok {
		return pythonruntime.CanonicalTarget{}, RootSpec{}, pythonruntime.ErrnoEACCES
	}
	return pythonruntime.CanonicalTarget{
		Namespace: pythonruntime.NamespaceWorkspace,
		RootID:    a.defaultRoot.ID,
		Branch:    a.defaultRoot.Branch,
		RelPath:   cleaned,
	}, a.defaultRoot, pythonruntime.ErrnoSuccess
}

func cleanConfinedRel(rel string) (string, bool) {
	if rel == "" || rel == "." {
		return "", true
	}
	cleaned := path.Clean(rel)
	if cleaned == "." {
		return "", true
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return strings.TrimPrefix(cleaned, "/"), true
}

func (a *OverlayAdapter) admit(ctx context.Context, family pythonruntime.EffectFamily, op string, target pythonruntime.CanonicalTarget, frame *pythonruntime.DeclaredFrame) (string, pythonruntime.Errno) {
	a.mu.Lock()
	opID := a.allocOpIDLocked()
	a.mu.Unlock()
	if a.observer == nil {
		return opID, pythonruntime.ErrnoSuccess
	}
	decision, err := a.observer.Admit(ctx, pythonruntime.ReachedOperation{
		OperationID:   opID,
		ExecutionID:   a.executionID,
		Family:        family,
		Operation:     op,
		Target:        target,
		DeclaredFrame: frame,
	})
	if err != nil {
		return opID, pythonruntime.ErrnoEACCES
	}
	if decision.Kind == pythonruntime.DecisionAllow {
		return opID, pythonruntime.ErrnoSuccess
	}
	if decision.Errno != pythonruntime.ErrnoSuccess {
		return opID, decision.Errno
	}
	return opID, pythonruntime.ErrnoEACCES
}

func (a *OverlayAdapter) recordOutcome(ctx context.Context, opID string, errno pythonruntime.Errno, bytes int64) {
	if a.observer == nil {
		return
	}
	a.observer.RecordOutcome(ctx, opID, pythonruntime.OperationOutcome{
		Succeeded:        errno == pythonruntime.ErrnoSuccess,
		Errno:            errno,
		BytesTransferred: bytes,
	})
}

func (a *OverlayAdapter) recordOverlayEventLocked(op string, target pythonruntime.CanonicalTarget, prev *pythonruntime.CanonicalTarget, bytesAffected int64, writeOrigin, flushClose *pythonruntime.DeclaredFrame) {
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
		ExecutionID:      a.executionID,
		OperationID:      opID,
		Sequence:         seq,
		Operation:        op,
		Target:           target,
		PreviousTarget:   prevCopy,
		BytesAffected:    bytesAffected,
		WriteOriginFrame: writeOrigin,
		FlushCloseFrame:  flushClose,
		RecordedAt:       a.now().UTC(),
	})
}

func (a *OverlayAdapter) recordDocumentRead(opID string, snap DocumentSnapshot, frame *pythonruntime.DeclaredFrame) {
	if a.evidence == nil {
		return
	}
	a.evidence.RecordDocumentRead(DocumentReadRecord{
		ExecutionID:           a.executionID,
		OperationID:           opID,
		Target:                snap.Target,
		Revision:              snap.Revision,
		BytesDeliveredToGuest: int64(len(snap.Bytes)),
		UnsavedDraft:          snap.Revision.UnsavedDraft,
		WriteOriginFrame:      frame,
		RecordedAt:            a.now().UTC(),
	})
}

func (a *OverlayAdapter) followSymlinksLocked(ctx context.Context, target pythonruntime.CanonicalTarget, followFinal bool) (pythonruntime.CanonicalTarget, pythonruntime.Errno) {
	if target.Namespace != pythonruntime.NamespaceWorkspace && target.Namespace != pythonruntime.NamespaceScratch {
		return target, pythonruntime.ErrnoSuccess
	}
	cur := target
	for hops := 0; hops < maxSymlinkHops; hops++ {
		if cur.RelPath == "" {
			return cur, pythonruntime.ErrnoSuccess
		}
		parts := strings.Split(cur.RelPath, "/")
		followed := false
		prefix := ""
		for i, part := range parts {
			if prefix == "" {
				prefix = part
			} else {
				prefix += "/" + part
			}
			isFinal := i == len(parts)-1
			if isFinal && !followFinal {
				break
			}
			probe := cur
			probe.RelPath = prefix
			linkTarget, isLink, errno := a.readlinkSingleLocked(ctx, probe)
			if errno != pythonruntime.ErrnoSuccess {
				if errno == pythonruntime.ErrnoENOENT {
					// Missing component; caller handles ENOENT/O_CREAT.
					return cur, pythonruntime.ErrnoSuccess
				}
				return pythonruntime.CanonicalTarget{}, errno
			}
			if !isLink {
				continue
			}
			var base string
			if strings.HasPrefix(linkTarget, "/") {
				base = strings.TrimPrefix(linkTarget, "/")
			} else {
				parent := path.Dir(prefix)
				if parent == "." {
					base = linkTarget
				} else {
					base = parent + "/" + linkTarget
				}
			}
			remaining := ""
			if i+1 < len(parts) {
				remaining = strings.Join(parts[i+1:], "/")
				if base == "" {
					base = remaining
				} else {
					base = base + "/" + remaining
				}
			}
			cleaned, ok := cleanConfinedRel(base)
			if !ok {
				return pythonruntime.CanonicalTarget{}, pythonruntime.ErrnoEACCES
			}
			cur.RelPath = cleaned
			followed = true
			break
		}
		if !followed {
			return cur, pythonruntime.ErrnoSuccess
		}
	}
	return pythonruntime.CanonicalTarget{}, pythonruntime.ErrnoEINVAL
}

func (a *OverlayAdapter) readlinkSingleLocked(ctx context.Context, target pythonruntime.CanonicalTarget) (string, bool, pythonruntime.Errno) {
	k := CanonicalKeyOf(target)
	if target.Namespace == pythonruntime.NamespaceScratch {
		n, ok := a.scratch[k]
		if !ok || n.kind == nodeTombstone {
			return "", false, pythonruntime.ErrnoENOENT
		}
		if n.kind == nodeSymlink {
			return n.linkTarget, true, pythonruntime.ErrnoSuccess
		}
		return "", false, pythonruntime.ErrnoSuccess
	}
	if n, ok := a.overlay[k]; ok {
		if n.kind == nodeTombstone {
			return "", false, pythonruntime.ErrnoENOENT
		}
		if n.kind == nodeSymlink {
			return n.linkTarget, true, pythonruntime.ErrnoSuccess
		}
		return "", false, pythonruntime.ErrnoSuccess
	}
	if a.store == nil {
		return "", false, pythonruntime.ErrnoENOENT
	}
	snap, errno := a.store.StatDocument(ctx, target)
	if errno != pythonruntime.ErrnoSuccess {
		return "", false, errno
	}
	if snap.IsSymlink {
		return snap.LinkTarget, true, pythonruntime.ErrnoSuccess
	}
	return "", false, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) ensureParentDirLocked(ctx context.Context, target pythonruntime.CanonicalTarget) pythonruntime.Errno {
	if target.RelPath == "" {
		return pythonruntime.ErrnoSuccess
	}
	parent := path.Dir(target.RelPath)
	if parent == "." || parent == "" {
		return pythonruntime.ErrnoSuccess
	}
	pt := target
	pt.RelPath = parent
	info, errno := a.statTargetLocked(ctx, pt)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	if !info.IsDir {
		return pythonruntime.ErrnoENOTDIR
	}
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) statTargetLocked(ctx context.Context, target pythonruntime.CanonicalTarget) (pythonruntime.StatInfo, pythonruntime.Errno) {
	switch target.Namespace {
	case pythonruntime.NamespaceNullDevice:
		return pythonruntime.StatInfo{
			Target:  target,
			Name:    "null",
			Mode:    fs.ModeDevice | fs.ModeCharDevice | 0o666,
			ModTime: a.now().UTC(),
		}, pythonruntime.ErrnoSuccess
	case pythonruntime.NamespaceRuntime, pythonruntime.NamespacePackages:
		k := CanonicalKeyOf(target)
		if data, ok := a.roFiles[k]; ok {
			return pythonruntime.StatInfo{
				Target:  target,
				Name:    path.Base(target.RelPath),
				Size:    int64(len(data)),
				Mode:    0o444,
				ModTime: a.now().UTC(),
			}, pythonruntime.ErrnoSuccess
		}
		if target.RelPath == "" {
			return pythonruntime.StatInfo{
				Target:  target,
				Name:    string(target.Namespace),
				Mode:    fs.ModeDir | 0o555,
				ModTime: a.now().UTC(),
				IsDir:   true,
			}, pythonruntime.ErrnoSuccess
		}
		return pythonruntime.StatInfo{}, pythonruntime.ErrnoENOENT
	case pythonruntime.NamespaceScratch:
		if target.RelPath == "" {
			return pythonruntime.StatInfo{
				Target:  target,
				Name:    "scratch",
				Mode:    fs.ModeDir | 0o755,
				ModTime: a.now().UTC(),
				IsDir:   true,
				Inode:   2,
			}, pythonruntime.ErrnoSuccess
		}
		n, ok := a.scratch[CanonicalKeyOf(target)]
		if !ok || n.kind == nodeTombstone {
			return pythonruntime.StatInfo{}, pythonruntime.ErrnoENOENT
		}
		return nodeStatInfo(n), pythonruntime.ErrnoSuccess
	}

	// NamespaceWorkspace
	if target.RelPath == "" {
		return pythonruntime.StatInfo{
			Target:  target,
			Name:    target.RootID,
			Mode:    fs.ModeDir | 0o755,
			ModTime: a.now().UTC(),
			IsDir:   true,
			Inode:   1,
		}, pythonruntime.ErrnoSuccess
	}
	k := CanonicalKeyOf(target)
	if n, ok := a.overlay[k]; ok {
		if n.kind == nodeTombstone {
			return pythonruntime.StatInfo{}, pythonruntime.ErrnoENOENT
		}
		return nodeStatInfo(n), pythonruntime.ErrnoSuccess
	}
	if a.store == nil {
		return pythonruntime.StatInfo{}, pythonruntime.ErrnoENOENT
	}
	snap, errno := a.store.StatDocument(ctx, target)
	if errno != pythonruntime.ErrnoSuccess {
		return pythonruntime.StatInfo{}, errno
	}
	return snapshotStatInfo(snap), pythonruntime.ErrnoSuccess
}

func nodeStatInfo(n *overlayNode) pythonruntime.StatInfo {
	n.mu.Lock()
	defer n.mu.Unlock()
	name := path.Base(n.target.RelPath)
	if n.target.RelPath == "" {
		name = n.target.RootID
	}
	return pythonruntime.StatInfo{
		Target:    n.target,
		Name:      name,
		Size:      int64(len(n.data)),
		Mode:      n.mode,
		ModTime:   n.modTime,
		IsDir:     n.kind == nodeDir,
		IsSymlink: n.kind == nodeSymlink,
		Inode:     n.inode,
	}
}

func snapshotStatInfo(snap DocumentSnapshot) pythonruntime.StatInfo {
	name := path.Base(snap.Target.RelPath)
	if snap.Target.RelPath == "" {
		name = snap.Target.RootID
	}
	return pythonruntime.StatInfo{
		Target:    snap.Target,
		Name:      name,
		Size:      int64(len(snap.Bytes)),
		Mode:      snap.Mode,
		ModTime:   snap.ModTime,
		IsDir:     snap.IsDir,
		IsSymlink: snap.IsSymlink,
		Inode:     snap.Inode,
	}
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

func (a *OverlayAdapter) Open(ctx context.Context, req pythonruntime.OpenRequest) (pythonruntime.GuestFile, pythonruntime.Errno) {
	target, rootSpec, errno := a.resolvePath(req.Path)
	if errno != pythonruntime.ErrnoSuccess {
		return nil, errno
	}
	isWrite := req.Flags&(pythonruntime.OpenWrite|pythonruntime.OpenCreate|pythonruntime.OpenTruncate|pythonruntime.OpenAppend) != 0
	family := pythonruntime.EffectFilesystemRead
	if isWrite {
		family = pythonruntime.EffectFilesystemWrite
	}
	opID, admitErrno := a.admit(ctx, family, "open", target, req.OriginFrame)
	if admitErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, admitErrno, 0)
		return nil, admitErrno
	}

	if target.Namespace == pythonruntime.NamespaceNullDevice {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
		return &nullGuestFile{target: target, now: a.now}, pythonruntime.ErrnoSuccess
	}

	if rootSpec.ReadOnly && isWrite {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEROFS, 0)
		return nil, pythonruntime.ErrnoEROFS
	}
	if target.Namespace == pythonruntime.NamespaceRuntime || target.Namespace == pythonruntime.NamespacePackages {
		k := CanonicalKeyOf(target)
		data, ok := a.roFiles[k]
		if !ok {
			a.recordOutcome(ctx, opID, pythonruntime.ErrnoENOENT, 0)
			return nil, pythonruntime.ErrnoENOENT
		}
		node := &overlayNode{
			kind:    nodeFile,
			target:  target,
			data:    append([]byte(nil), data...),
			mode:    0o444,
			modTime: a.now().UTC(),
		}
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, int64(len(data)))
		return &overlayFile{adapter: a, node: node, flags: req.Flags, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	followFinal := req.Flags&pythonruntime.OpenNoFollow == 0
	resolved, symErrno := a.followSymlinksLocked(ctx, target, followFinal)
	if symErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, symErrno, 0)
		return nil, symErrno
	}
	target = resolved

	if target.Namespace == pythonruntime.NamespaceScratch {
		f, fErrno := a.openScratchLocked(ctx, target, req)
		a.recordOutcome(ctx, opID, fErrno, 0)
		return f, fErrno
	}

	f, bytesDelivered, fErrno := a.openWorkspaceLocked(ctx, opID, target, req, isWrite)
	a.recordOutcome(ctx, opID, fErrno, bytesDelivered)
	return f, fErrno
}

func (a *OverlayAdapter) openScratchLocked(ctx context.Context, target pythonruntime.CanonicalTarget, req pythonruntime.OpenRequest) (pythonruntime.GuestFile, pythonruntime.Errno) {
	k := CanonicalKeyOf(target)
	n, exists := a.scratch[k]
	if exists && n.kind == nodeTombstone {
		exists = false
		n = nil
	}
	isWrite := req.Flags&(pythonruntime.OpenWrite|pythonruntime.OpenCreate|pythonruntime.OpenTruncate|pythonruntime.OpenAppend) != 0
	if exists {
		if req.Flags&(pythonruntime.OpenCreate|pythonruntime.OpenExclusive) == (pythonruntime.OpenCreate | pythonruntime.OpenExclusive) {
			return nil, pythonruntime.ErrnoEEXIST
		}
		if n.kind == nodeDir {
			if isWrite {
				return nil, pythonruntime.ErrnoEISDIR
			}
			return &overlayFile{adapter: a, node: n, flags: req.Flags, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
		}
		if req.Flags&pythonruntime.OpenDirectory != 0 {
			return nil, pythonruntime.ErrnoENOTDIR
		}
		if req.Flags&pythonruntime.OpenTruncate != 0 {
			n.mu.Lock()
			n.data = nil
			n.modTime = a.now().UTC()
			n.mu.Unlock()
		}
		return &overlayFile{adapter: a, node: n, flags: req.Flags, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
	}
	if req.Flags&pythonruntime.OpenCreate == 0 {
		return nil, pythonruntime.ErrnoENOENT
	}
	if errParent := a.ensureParentDirLocked(ctx, target); errParent != pythonruntime.ErrnoSuccess {
		return nil, errParent
	}
	mode := req.Mode
	if mode == 0 {
		mode = 0o644
	}
	n = &overlayNode{
		kind:             nodeFile,
		target:           target,
		mode:             mode,
		modTime:          a.now().UTC(),
		inode:            a.allocInodeLocked(),
		createdInOverlay: true,
		writeOriginFrame: req.OriginFrame,
	}
	a.scratch[k] = n
	return &overlayFile{adapter: a, node: n, flags: req.Flags, writeOriginFrame: req.OriginFrame}, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) openWorkspaceLocked(ctx context.Context, opID string, target pythonruntime.CanonicalTarget, req pythonruntime.OpenRequest, isWrite bool) (pythonruntime.GuestFile, int64, pythonruntime.Errno) {
	k := CanonicalKeyOf(target)
	ov, inOverlay := a.overlay[k]
	if inOverlay && ov.kind != nodeTombstone {
		if req.Flags&(pythonruntime.OpenCreate|pythonruntime.OpenExclusive) == (pythonruntime.OpenCreate | pythonruntime.OpenExclusive) {
			return nil, 0, pythonruntime.ErrnoEEXIST
		}
		if ov.kind == nodeDir {
			if isWrite {
				return nil, 0, pythonruntime.ErrnoEISDIR
			}
			return &overlayFile{adapter: a, node: ov, flags: req.Flags, writeOriginFrame: req.OriginFrame}, 0, pythonruntime.ErrnoSuccess
		}
		if req.Flags&pythonruntime.OpenDirectory != 0 {
			return nil, 0, pythonruntime.ErrnoENOTDIR
		}
		if req.Flags&pythonruntime.OpenTruncate != 0 {
			ov.mu.Lock()
			ov.data = nil
			ov.contentModified = true
			ov.modTime = a.now().UTC()
			if ov.writeOriginFrame == nil {
				ov.writeOriginFrame = req.OriginFrame
			}
			ov.mu.Unlock()
			ov.mutationSeq = a.allocSeqLocked()
			a.recordOverlayEventLocked("truncate", ov.target, nil, 0, req.OriginFrame, nil)
		}
		return &overlayFile{adapter: a, node: ov, flags: req.Flags, writeOriginFrame: req.OriginFrame}, int64(len(ov.data)), pythonruntime.ErrnoSuccess
	}

	// Not in overlay as a live entry. Check tombstone or host DocumentStore.
	hostExists := false
	var snap DocumentSnapshot
	if !inOverlay {
		origin := PinOriginRead
		if isWrite && req.Flags&pythonruntime.OpenRead == 0 {
			origin = PinOriginBlindWrite
		}
		var snapErrno pythonruntime.Errno
		snap, snapErrno = a.pinHostStateLocked(ctx, target, origin)
		if snapErrno == pythonruntime.ErrnoSuccess {
			hostExists = true
		} else if snapErrno != pythonruntime.ErrnoENOENT {
			return nil, 0, snapErrno
		}
	}

	if hostExists {
		if req.Flags&(pythonruntime.OpenCreate|pythonruntime.OpenExclusive) == (pythonruntime.OpenCreate | pythonruntime.OpenExclusive) {
			return nil, 0, pythonruntime.ErrnoEEXIST
		}
		if snap.IsDir {
			if isWrite {
				return nil, 0, pythonruntime.ErrnoEISDIR
			}
			dirNode := &overlayNode{
				kind:    nodeDir,
				target:  snap.Target,
				mode:    snap.Mode,
				modTime: snap.ModTime,
				inode:   snap.Inode,
			}
			return &overlayFile{adapter: a, node: dirNode, flags: req.Flags, writeOriginFrame: req.OriginFrame}, 0, pythonruntime.ErrnoSuccess
		}
		if req.Flags&pythonruntime.OpenDirectory != 0 {
			return nil, 0, pythonruntime.ErrnoENOTDIR
		}
		if req.Flags&pythonruntime.OpenRead != 0 || !isWrite {
			a.recordDocumentRead(opID, snap, req.OriginFrame)
		}
		data := append([]byte(nil), snap.Bytes...)
		if !isWrite {
			// Read-only open on unmodified host file: do NOT install a dirty
			// overlay node so subsequent helper rereads still observe live host
			// state while RevisionPinTable preserves the earliest pin.
			roNode := &overlayNode{
				kind:    nodeFile,
				target:  snap.Target,
				data:    data,
				mode:    snap.Mode,
				modTime: snap.ModTime,
				inode:   snap.Inode,
			}
			return &overlayFile{adapter: a, node: roNode, flags: req.Flags, writeOriginFrame: req.OriginFrame}, int64(len(data)), pythonruntime.ErrnoSuccess
		}
		if req.Flags&pythonruntime.OpenTruncate != 0 {
			data = nil
		}
		node := &overlayNode{
			kind:             nodeFile,
			target:           snap.Target,
			data:             data,
			mode:             snap.Mode,
			modTime:          a.now().UTC(),
			inode:            snap.Inode,
			createdInOverlay: false,
			contentModified:  req.Flags&pythonruntime.OpenTruncate != 0,
			mutationSeq:      a.allocSeqLocked(),
			writeOriginFrame: req.OriginFrame,
		}
		a.overlay[k] = node
		if req.Flags&pythonruntime.OpenTruncate != 0 {
			a.recordOverlayEventLocked("truncate", node.target, nil, 0, req.OriginFrame, nil)
		} else {
			a.recordOverlayEventLocked("open", node.target, nil, int64(len(data)), req.OriginFrame, nil)
		}
		return &overlayFile{adapter: a, node: node, flags: req.Flags, writeOriginFrame: req.OriginFrame}, int64(len(data)), pythonruntime.ErrnoSuccess
	}

	// File does not exist (either absent in host or tombstoned in overlay).
	if req.Flags&pythonruntime.OpenCreate == 0 {
		return nil, 0, pythonruntime.ErrnoENOENT
	}
	if errParent := a.ensureParentDirLocked(ctx, target); errParent != pythonruntime.ErrnoSuccess {
		return nil, 0, errParent
	}
	mode := req.Mode
	if mode == 0 {
		mode = 0o644
	}
	node := &overlayNode{
		kind:             nodeFile,
		target:           target,
		mode:             mode,
		modTime:          a.now().UTC(),
		inode:            a.allocInodeLocked(),
		createdInOverlay: true,
		contentModified:  true,
		mutationSeq:      a.allocSeqLocked(),
		writeOriginFrame: req.OriginFrame,
	}
	a.overlay[k] = node
	a.recordOverlayEventLocked("open", target, nil, 0, req.OriginFrame, nil)
	return &overlayFile{adapter: a, node: node, flags: req.Flags, writeOriginFrame: req.OriginFrame}, 0, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Stat(ctx context.Context, rawPath string) (pythonruntime.StatInfo, pythonruntime.Errno) {
	return a.statImpl(ctx, rawPath, true)
}

func (a *OverlayAdapter) Lstat(ctx context.Context, rawPath string) (pythonruntime.StatInfo, pythonruntime.Errno) {
	return a.statImpl(ctx, rawPath, false)
}

func (a *OverlayAdapter) statImpl(ctx context.Context, rawPath string, follow bool) (pythonruntime.StatInfo, pythonruntime.Errno) {
	target, _, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return pythonruntime.StatInfo{}, errno
	}
	opName := "stat"
	if !follow {
		opName = "lstat"
	}
	opID, admitErrno := a.admit(ctx, pythonruntime.EffectFilesystemMetadata, opName, target, nil)
	if admitErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, admitErrno, 0)
		return pythonruntime.StatInfo{}, admitErrno
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	resolved, symErrno := a.followSymlinksLocked(ctx, target, follow)
	if symErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, symErrno, 0)
		return pythonruntime.StatInfo{}, symErrno
	}
	info, stErrno := a.statTargetLocked(ctx, resolved)
	a.recordOutcome(ctx, opID, stErrno, 0)
	return info, stErrno
}

func (a *OverlayAdapter) ReadDir(ctx context.Context, rawPath string) ([]pythonruntime.DirEntry, pythonruntime.Errno) {
	target, _, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return nil, errno
	}
	opID, admitErrno := a.admit(ctx, pythonruntime.EffectFilesystemRead, "readdir", target, nil)
	if admitErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, admitErrno, 0)
		return nil, admitErrno
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	resolved, symErrno := a.followSymlinksLocked(ctx, target, true)
	if symErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, symErrno, 0)
		return nil, symErrno
	}
	entries, rdErrno := a.readDirLocked(ctx, resolved)
	a.recordOutcome(ctx, opID, rdErrno, 0)
	return entries, rdErrno
}

func (a *OverlayAdapter) readDirLocked(ctx context.Context, target pythonruntime.CanonicalTarget) ([]pythonruntime.DirEntry, pythonruntime.Errno) {
	info, errno := a.statTargetLocked(ctx, target)
	if errno != pythonruntime.ErrnoSuccess {
		return nil, errno
	}
	if !info.IsDir {
		return nil, pythonruntime.ErrnoENOTDIR
	}
	merged := make(map[string]pythonruntime.DirEntry)
	dirRel := cleanRelPath(target.RelPath)

	if target.Namespace == pythonruntime.NamespaceWorkspace && a.store != nil {
		hostEntries, hostErrno := a.store.ListDir(ctx, target)
		if hostErrno == pythonruntime.ErrnoSuccess {
			for _, e := range hostEntries {
				merged[e.Name] = e
			}
		}
	}

	var layer map[CanonicalKey]*overlayNode
	if target.Namespace == pythonruntime.NamespaceScratch {
		layer = a.scratch
	} else if target.Namespace == pythonruntime.NamespaceWorkspace {
		layer = a.overlay
	}
	for k, n := range layer {
		if k.Namespace != target.Namespace || k.RootID != target.RootID || k.Branch != target.Branch {
			continue
		}
		if k.RelPath == "" || k.RelPath == dirRel {
			continue
		}
		parent := path.Dir(k.RelPath)
		if parent == "." {
			parent = ""
		}
		if parent != dirRel {
			continue
		}
		name := path.Base(k.RelPath)
		if n.kind == nodeTombstone {
			delete(merged, name)
			continue
		}
		merged[name] = pythonruntime.DirEntry{
			Name:      name,
			IsDir:     n.kind == nodeDir,
			IsSymlink: n.kind == nodeSymlink,
			Mode:      n.mode,
		}
	}

	out := make([]pythonruntime.DirEntry, 0, len(merged))
	for _, e := range merged {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Mkdir(ctx context.Context, rawPath string, perm fs.FileMode) pythonruntime.Errno {
	target, rootSpec, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	opID, admitErrno := a.admit(ctx, pythonruntime.EffectFilesystemWrite, "mkdir", target, nil)
	if admitErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, admitErrno, 0)
		return admitErrno
	}
	if rootSpec.ReadOnly || target.Namespace == pythonruntime.NamespaceRuntime || target.Namespace == pythonruntime.NamespacePackages {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEROFS, 0)
		return pythonruntime.ErrnoEROFS
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, stErrno := a.statTargetLocked(ctx, target); stErrno == pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEEXIST, 0)
		return pythonruntime.ErrnoEEXIST
	}
	if parentErrno := a.ensureParentDirLocked(ctx, target); parentErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, parentErrno, 0)
		return parentErrno
	}
	if perm == 0 {
		perm = 0o755
	}
	node := &overlayNode{
		kind:             nodeDir,
		target:           target,
		mode:             perm | fs.ModeDir,
		modTime:          a.now().UTC(),
		inode:            a.allocInodeLocked(),
		createdInOverlay: true,
		mutationSeq:      a.allocSeqLocked(),
	}
	k := CanonicalKeyOf(target)
	if target.Namespace == pythonruntime.NamespaceScratch {
		a.scratch[k] = node
	} else {
		_, _ = a.pinHostStateLocked(ctx, target, PinOriginBlindWrite)
		a.overlay[k] = node
		a.recordOverlayEventLocked("mkdir", target, nil, 0, nil, nil)
	}
	a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Unlink(ctx context.Context, rawPath string) pythonruntime.Errno {
	target, rootSpec, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	opID, admitErrno := a.admit(ctx, pythonruntime.EffectFilesystemWrite, "unlink", target, nil)
	if admitErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, admitErrno, 0)
		return admitErrno
	}
	if rootSpec.ReadOnly || target.Namespace == pythonruntime.NamespaceRuntime || target.Namespace == pythonruntime.NamespacePackages {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEROFS, 0)
		return pythonruntime.ErrnoEROFS
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	resolved, symErrno := a.followSymlinksLocked(ctx, target, false)
	if symErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, symErrno, 0)
		return symErrno
	}
	target = resolved
	info, stErrno := a.statTargetLocked(ctx, target)
	if stErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, stErrno, 0)
		return stErrno
	}
	if info.IsDir {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEISDIR, 0)
		return pythonruntime.ErrnoEISDIR
	}
	k := CanonicalKeyOf(target)
	if target.Namespace == pythonruntime.NamespaceScratch {
		delete(a.scratch, k)
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
		return pythonruntime.ErrnoSuccess
	}
	existedInHost := a.existedInHostLocked(ctx, target)
	if existedInHost {
		_, _ = a.pinHostStateLocked(ctx, target, PinOriginBlindWrite)
		a.overlay[k] = &overlayNode{
			kind:        nodeTombstone,
			target:      target,
			mutationSeq: a.allocSeqLocked(),
		}
	} else {
		delete(a.overlay, k)
	}
	a.recordOverlayEventLocked("unlink", target, nil, 0, nil, nil)
	a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Rmdir(ctx context.Context, rawPath string) pythonruntime.Errno {
	target, rootSpec, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	opID, admitErrno := a.admit(ctx, pythonruntime.EffectFilesystemWrite, "rmdir", target, nil)
	if admitErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, admitErrno, 0)
		return admitErrno
	}
	if rootSpec.ReadOnly || target.Namespace == pythonruntime.NamespaceRuntime || target.Namespace == pythonruntime.NamespacePackages {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEROFS, 0)
		return pythonruntime.ErrnoEROFS
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	info, stErrno := a.statTargetLocked(ctx, target)
	if stErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, stErrno, 0)
		return stErrno
	}
	if !info.IsDir {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoENOTDIR, 0)
		return pythonruntime.ErrnoENOTDIR
	}
	entries, rdErrno := a.readDirLocked(ctx, target)
	if rdErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, rdErrno, 0)
		return rdErrno
	}
	if len(entries) > 0 {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEEXIST, 0)
		return pythonruntime.ErrnoEEXIST
	}
	k := CanonicalKeyOf(target)
	if target.Namespace == pythonruntime.NamespaceScratch {
		delete(a.scratch, k)
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
		return pythonruntime.ErrnoSuccess
	}
	if a.existedInHostLocked(ctx, target) {
		_, _ = a.pinHostStateLocked(ctx, target, PinOriginBlindWrite)
		a.overlay[k] = &overlayNode{
			kind:        nodeTombstone,
			target:      target,
			mode:        fs.ModeDir,
			mutationSeq: a.allocSeqLocked(),
		}
	} else {
		delete(a.overlay, k)
	}
	a.recordOverlayEventLocked("rmdir", target, nil, 0, nil, nil)
	a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Rename(ctx context.Context, oldPath, newPath string) pythonruntime.Errno {
	oldTarget, oldRoot, errno := a.resolvePath(oldPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	newTarget, newRoot, errno := a.resolvePath(newPath)
	if errno != pythonruntime.ErrnoSuccess {
		return errno
	}
	opID, admitErrno := a.admit(ctx, pythonruntime.EffectFilesystemWrite, "rename", newTarget, nil)
	if admitErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, admitErrno, 0)
		return admitErrno
	}
	if oldTarget.Namespace != newTarget.Namespace || oldTarget.RootID != newTarget.RootID || oldTarget.Branch != newTarget.Branch {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEXDEV, 0)
		return pythonruntime.ErrnoEXDEV
	}
	if oldRoot.ReadOnly || newRoot.ReadOnly || oldTarget.Namespace == pythonruntime.NamespaceRuntime || oldTarget.Namespace == pythonruntime.NamespacePackages {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEROFS, 0)
		return pythonruntime.ErrnoEROFS
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	var symErrno pythonruntime.Errno
	oldTarget, symErrno = a.followSymlinksLocked(ctx, oldTarget, false)
	if symErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, symErrno, 0)
		return symErrno
	}
	newTarget, symErrno = a.followSymlinksLocked(ctx, newTarget, false)
	if symErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, symErrno, 0)
		return symErrno
	}

	if errParent := a.ensureParentDirLocked(ctx, newTarget); errParent != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, errParent, 0)
		return errParent
	}

	oldKey := CanonicalKeyOf(oldTarget)
	newKey := CanonicalKeyOf(newTarget)

	if oldTarget.Namespace == pythonruntime.NamespaceScratch {
		n, ok := a.scratch[oldKey]
		if !ok || n.kind == nodeTombstone {
			a.recordOutcome(ctx, opID, pythonruntime.ErrnoENOENT, 0)
			return pythonruntime.ErrnoENOENT
		}
		if oldKey == newKey {
			a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
			return pythonruntime.ErrnoSuccess
		}
		delete(a.scratch, oldKey)
		n.mu.Lock()
		n.target = newTarget
		n.mu.Unlock()
		a.scratch[newKey] = n
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
		return pythonruntime.ErrnoSuccess
	}

	// Workspace rename.
	oldNode, inOverlay := a.overlay[oldKey]
	if inOverlay && oldNode.kind == nodeTombstone {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoENOENT, 0)
		return pythonruntime.ErrnoENOENT
	}
	if !inOverlay {
		snap, snapErrno := a.pinHostStateLocked(ctx, oldTarget, PinOriginBlindWrite)
		if snapErrno != pythonruntime.ErrnoSuccess {
			a.recordOutcome(ctx, opID, snapErrno, 0)
			return snapErrno
		}
		kind := nodeFile
		if snap.IsDir {
			kind = nodeDir
		} else if snap.IsSymlink {
			kind = nodeSymlink
		}
		orig := snap.Target
		oldNode = &overlayNode{
			kind:             kind,
			target:           snap.Target,
			data:             append([]byte(nil), snap.Bytes...),
			mode:             snap.Mode,
			modTime:          snap.ModTime,
			inode:            snap.Inode,
			linkTarget:       snap.LinkTarget,
			createdInOverlay: false,
			originTarget:     &orig,
		}
	}

	if oldKey == newKey {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
		return pythonruntime.ErrnoSuccess
	}

	// Pin destination earliest base (existing or absent) before replacing.
	_, _ = a.pinHostStateLocked(ctx, newTarget, PinOriginBlindWrite)

	// If moving an existing host file (not createdInOverlay), preserve its
	// original host source for PublishMove semantics.
	if !oldNode.createdInOverlay && oldNode.originTarget == nil {
		orig := oldTarget
		oldNode.originTarget = &orig
	}

	// Update oldKey in overlay: tombstone if it existed in host, else remove.
	if a.existedInHostLocked(ctx, oldTarget) {
		a.overlay[oldKey] = &overlayNode{
			kind:        nodeTombstone,
			target:      oldTarget,
			mutationSeq: a.allocSeqLocked(),
		}
	} else {
		delete(a.overlay, oldKey)
	}

	oldNode.mu.Lock()
	oldNode.target = newTarget
	oldNode.modTime = a.now().UTC()
	oldNode.mu.Unlock()
	oldNode.mutationSeq = a.allocSeqLocked()
	a.overlay[newKey] = oldNode

	prev := oldTarget
	a.recordOverlayEventLocked("rename", newTarget, &prev, int64(len(oldNode.data)), oldNode.writeOriginFrame, nil)
	a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, int64(len(oldNode.data)))
	return pythonruntime.ErrnoSuccess
}

func (a *OverlayAdapter) Readlink(ctx context.Context, rawPath string) (string, pythonruntime.Errno) {
	target, _, errno := a.resolvePath(rawPath)
	if errno != pythonruntime.ErrnoSuccess {
		return "", errno
	}
	opID, admitErrno := a.admit(ctx, pythonruntime.EffectFilesystemMetadata, "readlink", target, nil)
	if admitErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, admitErrno, 0)
		return "", admitErrno
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	resolved, symErrno := a.followSymlinksLocked(ctx, target, false)
	if symErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, symErrno, 0)
		return "", symErrno
	}
	linkTarget, isLink, rdErrno := a.readlinkSingleLocked(ctx, resolved)
	if rdErrno != pythonruntime.ErrnoSuccess {
		a.recordOutcome(ctx, opID, rdErrno, 0)
		return "", rdErrno
	}
	if !isLink {
		a.recordOutcome(ctx, opID, pythonruntime.ErrnoEINVAL, 0)
		return "", pythonruntime.ErrnoEINVAL
	}
	a.recordOutcome(ctx, opID, pythonruntime.ErrnoSuccess, 0)
	return linkTarget, pythonruntime.ErrnoSuccess
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
