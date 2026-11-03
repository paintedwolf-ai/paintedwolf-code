package fuselinux

import (
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const fuseRootID uint64 = 1

// ServerConfig configures an in-process Linux FUSE v7.31 server instance for a mounted root.
type ServerConfig struct {
	ViewID                string
	RootID                string
	MountPath             string
	FSName                string
	MountOptions          string
	OwnerUID              uint32
	OwnerGID              uint32
	MaxConcurrentRequests int
	MaxLockWaiters        int
	BackingDir            string
	OnMutationHook        func(opcode uint32, rel string)
	OnLockWait            func(unique, start, end uint64, waiting bool)
}

// NegotiatedProtocol records the kernel's FUSE_INIT offer and the capabilities granted to it.
type NegotiatedProtocol struct {
	KernelMajor, KernelMinor uint32
	GrantedFlags             uint32
}

type fuseNode struct {
	fuseID     uint64
	generation uint64
	corePrivID layeredfs.NodeID
	paths      map[string]struct{}
	unlinked   bool
}

func (n *fuseNode) primaryRel() (string, bool) {
	for p := range n.paths {
		return p, true
	}
	return "", false
}

type openHandleEntry struct {
	hid    layeredfs.HandleID
	fuseID uint64
	rel    string
	isDir  bool
	// writable handles are the only ones that can dirty kernel pages.
	writable bool
}

type fileLockEntry struct {
	owner   uint64
	fh      uint64
	start   uint64
	end     uint64
	typ     uint32
	pid     uint32
	isFlock bool
}

type rawDirent struct {
	ino  uint64
	typ  uint32
	name string
}

// fuseRequestState is the cancellation identity of one request between read and reply.
type fuseRequestState struct {
	unique   uint64
	canceled atomic.Bool
}

func (r *fuseRequestState) isCanceled() bool { return r.canceled.Load() }

type queuedRequest struct {
	state *fuseRequestState
	req   []byte
}

// Server serves the Linux FUSE v7.31 wire protocol backed directly by *layeredfs.Core.
type Server struct {
	cfg      ServerConfig
	core     *layeredfs.Core
	fd       int
	stopPipe [2]int

	notifyMu  sync.RWMutex
	closed    atomic.Bool
	stopCh    chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
	wg        sync.WaitGroup

	negotiated      atomic.Pointer[NegotiatedProtocol]
	pollProbeActive atomic.Bool
	pollRequests    atomic.Int64
	syncBacking     func(interface{ Sync() error }) error

	nodeMu          sync.RWMutex
	nextFuseID      uint64
	byFuseID        map[uint64]*fuseNode
	byRel           map[string]*fuseNode
	byCorePrivateID map[layeredfs.NodeID]*fuseNode
	openHandles     map[uint64]openHandleEntry
	dirSnapshots    map[uint64][]rawDirent
	dirtyRels       map[string]struct{}
	nextDirFH       uint64

	lockMu          sync.Mutex
	lockCond        *sync.Cond
	fileLocks       map[uint64][]fileLockEntry
	lockWaiterCount int
	maxLockWaiters  int

	ordinaryQueue chan queuedRequest
	workerCount   int
	controlSem    chan struct{}
	reqMu         sync.Mutex
	inFlightReqs  map[uint64]*fuseRequestState

	gateMu        sync.Mutex
	gateCond      *sync.Cond
	barrierActive bool
	inFlightMut   int64
	gatedMut      int64
}

func newServerInstance(cfg ServerConfig, core *layeredfs.Core, fd int, stopPipe [2]int) *Server {
	maxReq := cfg.MaxConcurrentRequests
	if maxReq <= 0 {
		maxReq = 64
	}
	maxWaiters := cfg.MaxLockWaiters
	if maxWaiters <= 0 {
		maxWaiters = maxReq
	}
	rootNode := &fuseNode{
		fuseID:     fuseRootID,
		generation: 1,
		paths:      map[string]struct{}{".": {}},
	}
	s := &Server{
		cfg:             cfg,
		core:            core,
		fd:              fd,
		stopPipe:        stopPipe,
		nextFuseID:      2,
		byFuseID:        map[uint64]*fuseNode{fuseRootID: rootNode},
		byRel:           map[string]*fuseNode{".": rootNode},
		byCorePrivateID: make(map[layeredfs.NodeID]*fuseNode),
		openHandles:     make(map[uint64]openHandleEntry),
		dirtyRels:       make(map[string]struct{}),
		nextDirFH:       1 << 50,
		fileLocks:       make(map[uint64][]fileLockEntry),
		maxLockWaiters:  maxWaiters,
		stopCh:          make(chan struct{}),
		syncBacking:     func(f interface{ Sync() error }) error { return f.Sync() },
		dirSnapshots:    make(map[uint64][]rawDirent),
		ordinaryQueue:   make(chan queuedRequest, 2*maxReq),
		workerCount:     maxReq,
		controlSem:      make(chan struct{}, 32),
		inFlightReqs:    make(map[uint64]*fuseRequestState),
	}
	s.lockCond = sync.NewCond(&s.lockMu)
	s.gateCond = sync.NewCond(&s.gateMu)
	return s
}

func (s *Server) recordDirtyRel(rel string) {
	clean := cleanFuseRel(rel)
	s.nodeMu.Lock()
	s.dirtyRels[clean] = struct{}{}
	if clean != "." {
		if parent := path.Dir(clean); parent != "" {
			s.dirtyRels[cleanFuseRel(parent)] = struct{}{}
		}
	}
	s.nodeMu.Unlock()
}

// DirtyCandidatePaths returns the sorted union of relative paths mutated or held writable via FUSE.
func (s *Server) DirtyCandidatePaths() []string {
	if s == nil {
		return nil
	}
	s.nodeMu.RLock()
	seen := make(map[string]struct{}, len(s.dirtyRels)+len(s.openHandles))
	for r := range s.dirtyRels {
		seen[r] = struct{}{}
	}
	for _, h := range s.openHandles {
		if h.writable && h.rel != "" {
			seen[cleanFuseRel(h.rel)] = struct{}{}
		}
	}
	s.nodeMu.RUnlock()
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// NegotiatedProtocol returns the completed FUSE_INIT exchange, or nil before it has happened.
func (s *Server) NegotiatedProtocol() *NegotiatedProtocol { return s.negotiated.Load() }

// IsAlive reports whether the FUSE server is still running and serving /dev/fuse.
func (s *Server) IsAlive() bool {
	return s != nil && !s.closed.Load()
}

// IsBarrierActive reports whether the FUSE server is currently gating mutations for a checkpoint barrier.
func (s *Server) IsBarrierActive() bool {
	if s == nil {
		return false
	}
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	return s.barrierActive
}

// InFlightMutations returns the number of currently executing FUSE mutating operations.
func (s *Server) InFlightMutations() int64 {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	return s.inFlightMut
}

// InvalidateRelPath sends kernel dentry/inode cache invalidation notifications for rel.
func (s *Server) InvalidateRelPath(rel string) {
	clean := cleanFuseRel(rel)
	s.nodeMu.RLock()
	fn := s.byRel[clean]
	parentRel := path.Dir(clean)
	if parentRel == "" {
		parentRel = "."
	}
	parentFn := s.byRel[parentRel]
	s.nodeMu.RUnlock()
	if fn != nil {
		notifyInvalInode(s, fn.fuseID)
	}
	if parentFn != nil && clean != "." {
		notifyInvalEntry(s, parentFn.fuseID, path.Base(clean))
	}
}

func cleanFuseRel(rel string) string {
	rel = strings.TrimPrefix(path.Clean(strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/")), "/")
	if rel == "" || rel == "." {
		return "."
	}
	return rel
}
