package fuselinux

import (
	"path"
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

// lockTable is the view's POSIX and flock state. Every operation on it touches only this memory.
type lockTable struct {
	mu         sync.Mutex
	cond       *sync.Cond
	closed     *atomic.Bool
	files      map[uint64][]fileLockEntry
	waiters    int
	maxWaiters int
}

func newLockTable(maxWaiters int) *lockTable {
	l := &lockTable{files: make(map[uint64][]fileLockEntry), maxWaiters: maxWaiters}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// requestTable holds every request between read and reply, for cancellation by identity.
type requestTable struct {
	mu       sync.Mutex
	inFlight map[uint64]*fuseRequestState
}

func (t *requestTable) register(unique uint64) *fuseRequestState {
	st := &fuseRequestState{unique: unique}
	t.mu.Lock()
	t.inFlight[unique] = st
	t.mu.Unlock()
	return st
}

func (t *requestTable) unregister(unique uint64) {
	t.mu.Lock()
	delete(t.inFlight, unique)
	t.mu.Unlock()
}

func (t *requestTable) markCanceled(unique uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.inFlight[unique]
	if ok {
		st.canceled.Store(true)
	}
	return ok
}

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

	nodes *nodeTable

	locks *lockTable

	ordinaryQueue chan queuedRequest
	workerCount   int
	controlSem    chan struct{}
	requests      *requestTable
	gate          *mutationGate
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
	s := &Server{
		cfg:           cfg,
		core:          core,
		fd:            fd,
		stopPipe:      stopPipe,
		nodes:         newNodeTable(),
		locks:         newLockTable(maxWaiters),
		stopCh:        make(chan struct{}),
		syncBacking:   func(f interface{ Sync() error }) error { return f.Sync() },
		ordinaryQueue: make(chan queuedRequest, 2*maxReq),
		workerCount:   maxReq,
		controlSem:    make(chan struct{}, 32),
		requests:      &requestTable{inFlight: make(map[uint64]*fuseRequestState)},
	}
	s.locks.closed = &s.closed
	s.gate = newMutationGate(&s.closed)
	return s
}

// NegotiatedProtocol returns the completed FUSE_INIT exchange, or nil before it has happened.
func (s *Server) NegotiatedProtocol() *NegotiatedProtocol { return s.negotiated.Load() }

// IsAlive reports whether the FUSE server is still running and serving /dev/fuse.
func (s *Server) IsAlive() bool {
	return s != nil && !s.closed.Load()
}

// IsBarrierActive reports whether the FUSE server is currently gating mutations for a checkpoint barrier.
func (s *Server) IsBarrierActive() bool {
	return s != nil && s.gate.isActive()
}

// InvalidateRelPath sends kernel dentry/inode cache invalidation notifications for rel.
func (s *Server) InvalidateRelPath(rel string) {
	clean := cleanFuseRel(rel)
	s.nodes.mu.RLock()
	fn := s.nodes.byRel[clean]
	parentRel := path.Dir(clean)
	if parentRel == "" {
		parentRel = "."
	}
	parentFn := s.nodes.byRel[parentRel]
	s.nodes.mu.RUnlock()
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
