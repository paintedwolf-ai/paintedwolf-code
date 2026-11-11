package layeredfs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Core is the platform-neutral layered filesystem engine and mutation journal.
type Core struct {
	cfg      Config
	backing  *backingWorkspace
	roots    *rootTable
	counters *statsCounters

	mu      sync.RWMutex
	state   *viewState
	barrier *barrierCoordinator
}

type backingWorkspace struct {
	mu             sync.RWMutex
	snapshotMu     sync.Mutex
	tmpSeq         atomic.Uint64
	workDir        string
	baseDir        string
	journalDir     string
	workDirFile    *os.File
	baseDirFile    *os.File
	journalDirFile *os.File
	// parked means the directories are closed while the view is idle, so the volume holding them
	// can be ejected. parkedAs is what reopening them must find again.
	parked   bool
	parkedAs [3]os.FileInfo
}

// park syncs and closes the backing directories.
func (b *backingWorkspace) park() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.parked {
		return
	}
	for i, f := range []*os.File{b.workDirFile, b.baseDirFile, b.journalDirFile} {
		if f == nil {
			continue
		}
		_ = f.Sync()
		if info, err := f.Stat(); err == nil {
			b.parkedAs[i] = info
		}
		_ = f.Close()
	}
	b.workDirFile, b.baseDirFile, b.journalDirFile = nil, nil, nil
	b.parked = true
}

// unpark reopens parked directories, refusing any that is no longer the directory it was.
func (b *backingWorkspace) unpark() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.parked {
		return nil
	}
	var files [3]*os.File
	closeAll := func() {
		for _, f := range files {
			if f != nil {
				_ = f.Close()
			}
		}
	}
	for i, dir := range []string{b.workDir, b.baseDir, b.journalDir} {
		f, err := openDirNoFollow(dir)
		if err != nil {
			closeAll()
			return fmt.Errorf("%w: %w", ErrBackingUnavailable, err)
		}
		files[i] = f
		info, err := f.Stat()
		if err != nil || b.parkedAs[i] == nil || !os.SameFile(info, b.parkedAs[i]) {
			closeAll()
			return fmt.Errorf("%w: %s is no longer the view's backing", ErrBackingUnavailable, dir)
		}
	}
	b.workDirFile, b.baseDirFile, b.journalDirFile = files[0], files[1], files[2]
	b.parked = false
	return nil
}

type rootTable struct {
	specs        []RootSpec
	byID         map[string]RootSpec
	providers    map[string]LowerProvider
	ownedOS      []*osLowerProvider
	activeRootID string
}

type statsCounters struct {
	lowerDirReads atomic.Int64
	lowerLookups  atomic.Int64
	// lowerContentReads counts lower files opened for their content, outside Git metadata.
	lowerContentReads atomic.Int64
	physicalCopyUps   atomic.Int64
	capturedBaseBytes atomic.Int64
	journalEntries    atomic.Int64
	flushes           atomic.Int64
}

type idAllocator struct {
	closing       bool
	nextNodeID    uint64
	nextGen       uint64
	nextHandleID  uint64
	nextStorageID uint64
	nextSeq       uint64
}

type viewState struct {
	released     bool
	nsGen        uint64
	dirtySeq     uint64
	seq          idAllocator
	nodesByID    map[NodeID]*privateNode
	originIndex  map[NodeID]NodeID
	pathIndex    map[QualifiedPath]NodeID
	transient    map[QualifiedPath]struct{}
	lowerDirMod  map[QualifiedPath]time.Time
	deletedPaths map[QualifiedPath]deletedState
	// spellings finds a private path or deletion under any spelling a folding root treats as its own.
	spellings         spellingIndex
	basesByPath       map[QualifiedPath]BaseCapture
	handles           map[HandleID]*openHandle
	dirCookies        map[uint64]dirCookieState
	dirtyBackingFiles map[string]uint64
	journal           []JournalOperation
}

type nodeFlags struct {
	ioMu            sync.Mutex
	unlinked        bool
	physicalCopyUp  bool
	contentChanged  bool
	metadataChanged bool
	namespaceAdded  bool
	backingName     string
	backingPath     string
	gitHostPath     string
	linkTarget      string
}

func markDirtyBackingFileLocked(c *Core, path string) {
	if path == "" || c.state == nil {
		return
	}
	c.state.dirtySeq++
	c.state.dirtyBackingFiles[path] = c.state.dirtySeq
}

type nodeInitialState struct {
	sha256     string
	size       int64
	mode       fs.FileMode
	mtime      time.Time
	xattrs     map[string][]byte
	originPath *QualifiedPath
}

type privateNode struct {
	id      NodeID
	origin  NodeID
	gen     Generation
	kind    EntryKind
	mode    fs.FileMode
	size    int64
	atime   time.Time
	mtime   time.Time
	ctime   time.Time
	xattrs  map[string][]byte
	paths   map[QualifiedPath]struct{}
	flags   nodeFlags
	initial nodeInitialState
}

type deletedState struct {
	ReplacedKind EntryKind `json:"replaced_kind"`
	Timestamp    time.Time `json:"timestamp"`
}

type openHandle struct {
	id           HandleID
	viewID       string
	nodeID       NodeID
	gen          Generation
	qpath        QualifiedPath
	layer        LayerSource
	flags        OpenFlags
	isDir        bool
	closed       bool
	refs         int
	unlinkOrphan string
	file         *os.File
	roReader     ReadSeekCloser
}

type dirCookieState struct {
	nodeID  NodeID
	version uint64
}

// New creates and initializes a layered filesystem Core view.
func New(cfg Config) (*Core, error) {
	if cfg.ViewID == "" || cfg.BackingDir == "" || len(cfg.Roots) == 0 {
		return nil, fmt.Errorf("layeredfs: ViewID, BackingDir, and at least one RootSpec are required")
	}
	if cfg.MaxCaptureRetries <= 0 {
		cfg.MaxCaptureRetries = 4
	}
	backing, err := openBackingWorkspace(cfg.BackingDir)
	if err != nil {
		return nil, err
	}
	roots, err := initRootTable(cfg)
	if err != nil {
		backing.close()
		return nil, err
	}
	barrier := newBarrierCoordinator(cfg.ViewID)
	barrier.resume = func() error {
		if err := backing.unpark(); err != nil {
			return err
		}
		for _, provider := range roots.ownedOS {
			if err := provider.unpark(); err != nil {
				return err
			}
		}
		return nil
	}
	return &Core{
		cfg:      cfg,
		backing:  backing,
		roots:    roots,
		counters: &statsCounters{},
		state: &viewState{
			nodesByID:         make(map[NodeID]*privateNode),
			originIndex:       make(map[NodeID]NodeID),
			pathIndex:         make(map[QualifiedPath]NodeID),
			transient:         make(map[QualifiedPath]struct{}),
			lowerDirMod:       make(map[QualifiedPath]time.Time),
			deletedPaths:      make(map[QualifiedPath]deletedState),
			spellings:         spellingIndex{},
			basesByPath:       make(map[QualifiedPath]BaseCapture),
			handles:           make(map[HandleID]*openHandle),
			dirCookies:        make(map[uint64]dirCookieState),
			dirtyBackingFiles: make(map[string]uint64),
		},
		barrier: barrier,
	}, nil
}

func openBackingWorkspace(rootDir string) (*backingWorkspace, error) {
	workDir := filepath.Join(rootDir, "working")
	baseDir := filepath.Join(rootDir, "bases")
	journalDir := filepath.Join(rootDir, "journal")
	for _, d := range []string{workDir, baseDir, journalDir} {
		if err := mkdirBackingDir(d, 0o700); err != nil {
			return nil, fmt.Errorf("layeredfs: create backing dir %s: %w", d, err)
		}
	}
	workFile, err := openDirNoFollow(workDir)
	if err != nil {
		return nil, err
	}
	baseFile, err := openDirNoFollow(baseDir)
	if err != nil {
		_ = workFile.Close()
		return nil, err
	}
	journalFile, err := openDirNoFollow(journalDir)
	if err != nil {
		_ = workFile.Close()
		_ = baseFile.Close()
		return nil, err
	}
	return &backingWorkspace{
		workDir:        workFile.Name(),
		baseDir:        baseFile.Name(),
		journalDir:     journalFile.Name(),
		workDirFile:    workFile,
		baseDirFile:    baseFile,
		journalDirFile: journalFile,
	}, nil
}

func (b *backingWorkspace) close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, f := range []*os.File{b.workDirFile, b.baseDirFile, b.journalDirFile} {
		if f != nil {
			_ = f.Close()
		}
	}
	b.workDirFile, b.baseDirFile, b.journalDirFile = nil, nil, nil
}

func initRootTable(cfg Config) (*rootTable, error) {
	rt := &rootTable{
		specs:        append([]RootSpec(nil), cfg.Roots...),
		byID:         make(map[string]RootSpec, len(cfg.Roots)),
		providers:    make(map[string]LowerProvider, len(cfg.Roots)),
		activeRootID: cfg.ActiveRootID,
	}
	var primaryID string
	for i, r := range cfg.Roots {
		if r.ID == "" {
			rt.close()
			return nil, ErrInvalidRoot
		}
		r.Hidden = cleanHiddenRels(r.Hidden)
		if !r.Folding.Folds() && r.TrunkPath != "" {
			probe := cfg.FoldingProbe
			if probe == nil {
				probe = ProbeNameFolding
			}
			r.Folding = probe(r.TrunkPath)
		}
		rt.specs[i] = r
		rt.byID[r.ID] = r
		if r.IsPrimary && primaryID == "" {
			primaryID = r.ID
		}
		if r.TrunkPath != "" {
			rootID := r.ID
			prov, err := newOSLowerProvider(
				r.TrunkPath,
				func(rel, comp string) {
					if cfg.LookupRaceHook != nil {
						cfg.LookupRaceHook(QualifiedPath{RootID: rootID, Rel: rel}, comp)
					}
				},
				func(rel string) error {
					if cfg.FaultHooks != nil && cfg.FaultHooks.LowerXattrHook != nil {
						return cfg.FaultHooks.LowerXattrHook(QualifiedPath{RootID: rootID, Rel: rel})
					}
					return nil
				},
			)
			if err != nil {
				rt.close()
				return nil, fmt.Errorf("layeredfs: open root %s: %w", r.ID, err)
			}
			rt.ownedOS = append(rt.ownedOS, prov)
			rt.providers[r.ID] = prov
		}
	}
	if rt.activeRootID == "" {
		if primaryID != "" {
			rt.activeRootID = primaryID
		} else {
			rt.activeRootID = cfg.Roots[0].ID
		}
	}
	return rt, nil
}

func (rt *rootTable) close() {
	if rt == nil {
		return
	}
	for _, p := range rt.ownedOS {
		_ = p.close()
	}
	rt.ownedOS = nil
}

func (s *viewState) allocNodeID() (NodeID, Generation) {
	s.seq.nextNodeID++
	s.seq.nextGen++
	return NodeID(s.seq.nextNodeID), Generation(s.seq.nextGen)
}

func (s *viewState) allocHandleID() HandleID {
	s.seq.nextHandleID++
	return HandleID(s.seq.nextHandleID)
}

func (s *viewState) allocStorageFile(workDir, prefix string) (string, string) {
	s.seq.nextStorageID++
	name := fmt.Sprintf("%s-%08d", prefix, s.seq.nextStorageID)
	return name, filepath.Join(workDir, name)
}

func emitObservation(c *Core, ev ObservationEvent) {
	if c.cfg.ObservationSink == nil {
		return
	}
	ev.ViewID = c.cfg.ViewID
	c.cfg.ObservationSink.RecordObservation(ev)
}
