package fuselinux

import (
	"context"
	"errors"
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
	ViewID         string
	RootID         string
	MountPath      string
	FSName         string
	MountOptions   string
	OwnerUID       uint32
	OwnerGID       uint32
	OnMutationHook func(opcode uint32, rel string)
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

// Server serves the Linux FUSE v7.31 wire protocol backed directly by *layeredfs.Core.
type Server struct {
	cfg      ServerConfig
	core     *layeredfs.Core
	fd       int
	stopPipe [2]int

	writeMu sync.Mutex
	closed  atomic.Bool
	wg      sync.WaitGroup

	nodeMu          sync.RWMutex
	nextFuseID      uint64
	byFuseID        map[uint64]*fuseNode
	byRel           map[string]*fuseNode
	byCorePrivateID map[layeredfs.NodeID]*fuseNode
	openHandles     map[uint64]openHandleEntry
	dirtyRels       map[string]struct{}
	nextDirFH       uint64

	lockMu    sync.Mutex
	lockCond  *sync.Cond
	fileLocks map[uint64][]fileLockEntry

	gateMu        sync.Mutex
	gateCond      *sync.Cond
	barrierActive bool
	inFlightMut   int64
}

func newServerInstance(cfg ServerConfig, core *layeredfs.Core, fd int, stopPipe [2]int) *Server {
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

// DirtyCandidatePaths returns the sorted union of relative paths opened or mutated via FUSE.
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
		if h.rel != "" {
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

func (s *Server) enterMutation() bool {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	if s.closed.Load() || s.barrierActive {
		return false
	}
	s.inFlightMut++
	return true
}

func (s *Server) leaveMutation() {
	s.gateMu.Lock()
	s.inFlightMut--
	if s.inFlightMut <= 0 {
		s.inFlightMut = 0
		s.gateCond.Broadcast()
	}
	s.gateMu.Unlock()
}

// BeginBarrierDrain blocks admission of new mutating FUSE requests, waits for in-flight
// mutating FUSE requests to finish, and flushes Core journal state before Core.BeginCheckpointBarrier.
func (s *Server) BeginBarrierDrain(ctx context.Context, viewID string) (func(), error) {
	if !s.IsAlive() {
		return nil, errors.New("fuselinux: FUSE server is not alive")
	}
	s.gateMu.Lock()
	if s.barrierActive {
		s.gateMu.Unlock()
		return nil, layeredfs.ErrBarrierActive
	}
	s.barrierActive = true
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.gateMu.Lock()
			s.gateCond.Broadcast()
			s.gateMu.Unlock()
		case <-done:
		}
	}()
	for s.inFlightMut > 0 {
		if s.closed.Load() {
			s.barrierActive = false
			s.gateCond.Broadcast()
			s.gateMu.Unlock()
			close(done)
			return nil, errors.New("fuselinux: server closed during barrier drain")
		}
		if err := ctx.Err(); err != nil {
			s.barrierActive = false
			s.gateCond.Broadcast()
			s.gateMu.Unlock()
			close(done)
			return nil, err
		}
		s.gateCond.Wait()
	}
	s.gateMu.Unlock()
	close(done)

	if err := s.FlushForCheckpoint(ctx, viewID); err != nil {
		s.gateMu.Lock()
		s.barrierActive = false
		s.gateCond.Broadcast()
		s.gateMu.Unlock()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.gateMu.Lock()
			s.barrierActive = false
			s.gateCond.Broadcast()
			s.gateMu.Unlock()
		})
	}, nil
}

// FlushForCheckpoint syncs all open handles tracked by the FUSE server and snapshots Core's journal.
func (s *Server) FlushForCheckpoint(_ context.Context, _ string) error {
	if !s.IsAlive() {
		return errors.New("fuselinux: FUSE server is not alive")
	}
	s.nodeMu.RLock()
	hids := make([]layeredfs.HandleID, 0, len(s.openHandles))
	for _, h := range s.openHandles {
		if !h.isDir && h.hid != 0 {
			hids = append(hids, h.hid)
		}
	}
	s.nodeMu.RUnlock()
	for _, hid := range hids {
		if err := s.core.SyncHandle(hid); err != nil && !errors.Is(err, layeredfs.ErrStaleHandle) {
			return err
		}
	}
	_, err := s.core.SnapshotJournal()
	return err
}

// Close stops the FUSE server, wakes any blocked pollers/locks, and closes /dev/fuse.
func (s *Server) Close() error {
	if s == nil || !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.stopPipe[1] >= 0 {
		closeRawFD(s.stopPipe[1])
	}
	s.gateMu.Lock()
	s.gateCond.Broadcast()
	s.gateMu.Unlock()
	s.lockMu.Lock()
	s.lockCond.Broadcast()
	s.lockMu.Unlock()

	s.wg.Wait()
	if s.stopPipe[0] >= 0 {
		closeRawFD(s.stopPipe[0])
	}
	if s.fd >= 0 {
		closeRawFD(s.fd)
	}
	return nil
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
