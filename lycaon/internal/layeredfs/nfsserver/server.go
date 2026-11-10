package nfsserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const acceptRetryDelay = 50 * time.Millisecond

type exportEntry struct {
	cap      ExportCapability
	core     *layeredfs.Core
	viewHash uint64
	mounted  bool
}

type serverState struct {
	mu          sync.RWMutex
	cores       map[ViewID]*layeredfs.Core
	readOnly    map[ViewID]bool
	exportsByID map[string]*exportEntry
	exportsByVH map[uint64][]*exportEntry
	conns       map[net.Conn]struct{}
	closed      atomic.Bool
	wg          sync.WaitGroup
}

// Server is a loopback-only NFSv3 and MOUNTv3 TCP server backed by *layeredfs.Core.
type Server struct {
	core      *layeredfs.Core
	cfg       Config
	listener  net.Listener
	handles   *handleTable
	dirs      *dirCache
	stability *stabilityEngine
	gate      *serverBarrierGate
	workSem   chan struct{}
	queue     chan func()
	stopped   chan struct{}
	state     serverState
}

// New constructs and starts a loopback-only NFSv3 + MOUNTv3 server over core.
func New(core *layeredfs.Core, cfg Config) (*Server, error) {
	if core == nil {
		return nil, errors.New("layeredfs/nfsserver: nil Core")
	}
	cfg = cfg.withDefaults()
	if err := validateLoopbackListenAddr(cfg.ListenAddr); err != nil {
		return nil, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, fmt.Errorf("layeredfs/nfsserver: generate handle secret: %w", err)
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, err
	}
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); !ok || !tcpAddr.IP.IsLoopback() {
		_ = ln.Close()
		return nil, ErrNonLoopbackAddress
	}
	s := &Server{
		core: core, cfg: cfg, listener: ln,
		dirs:      newDirCache(secret, DefaultMaxDirListings),
		stability: newStabilityEngine(cfg), gate: newServerBarrierGate(),
		workSem: make(chan struct{}, DefaultMaxConcurrentRPCs), queue: make(chan func(), DefaultMaxQueuedRPCs),
		stopped: make(chan struct{}),
		state: serverState{
			cores: map[ViewID]*layeredfs.Core{core.ViewID(): core}, readOnly: make(map[ViewID]bool),
			exportsByID: make(map[string]*exportEntry), exportsByVH: make(map[uint64][]*exportEntry),
			conns: make(map[net.Conn]struct{}),
		},
	}
	s.handles = newHandleTable(secret, cfg.MaxHandles, s.stability.sidecars, func(viewID ViewID) *layeredfs.Core { return coreForView(s, viewID) })
	s.state.wg.Add(2)
	go runDispatchLoop(s)
	go runAcceptLoop(s)
	return s, nil
}

func validateLoopbackListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return fmt.Errorf("%w: %v", ErrNonLoopbackAddress, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return ErrNonLoopbackAddress
	}
	return nil
}

// Addr returns the bound loopback TCP address of the server.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// BindViewCore associates a dedicated *layeredfs.Core and read-only policy with viewID on s.
func BindViewCore(s *Server, viewID ViewID, core *layeredfs.Core, readOnly bool) {
	s.state.mu.Lock()
	if core != nil {
		s.state.cores[viewID] = core
	}
	s.state.readOnly[viewID] = readOnly
	s.state.mu.Unlock()
}

func coreForView(s *Server, viewID ViewID) *layeredfs.Core {
	s.state.mu.RLock()
	c := s.state.cores[viewID]
	s.state.mu.RUnlock()
	if c != nil {
		return c
	}
	return s.core
}

// RegisterExport registers a per-mount capability export for viewID and root using
// the server's configured ExpectedUID and ExpectedGID.
func (s *Server) RegisterExport(viewID ViewID, root RootID, token string) (ExportCapability, error) {
	return s.RegisterExportWithUID(viewID, root, token, s.cfg.ExpectedUID, s.cfg.ExpectedGID)
}

// RegisterExportWithUID registers a per-mount capability export bound to a specific caller UID/GID.
func (s *Server) RegisterExportWithUID(
	viewID ViewID, root RootID, token string, allowedUID, allowedGID uint32,
) (ExportCapability, error) {
	if viewID == "" {
		return ExportCapability{}, layeredfs.ErrInvalidRoot
	}
	if root == "" {
		root = ProjectRoot
	}
	if token == "" {
		var tokBytes [16]byte
		if _, err := rand.Read(tokBytes[:]); err != nil {
			return ExportCapability{}, err
		}
		token = hex.EncodeToString(tokBytes[:])
	}
	viewCore := coreForView(s, viewID)
	rootQPath := layeredfs.QualifiedPath{RootID: root, Rel: "."}
	meta, err := viewCore.Lstat(rootQPath)
	if err != nil {
		return ExportCapability{}, err
	}
	s.state.mu.RLock()
	ro := s.state.readOnly[viewID]
	s.state.mu.RUnlock()
	exportPath := fmt.Sprintf("/views/%s/%s/%s", viewID, root, token)
	capCfg := ExportCapability{
		ViewID: viewID, Root: root, Token: token,
		ExportPath: exportPath, AllowedUID: allowedUID, AllowedGID: allowedGID, ReadOnly: ro,
	}
	vHash := s.handles.RegisterView(viewID)
	s.handles.Ensure(viewID, rootQPath, meta, 0, true)
	entry := &exportEntry{cap: capCfg, core: viewCore, viewHash: vHash}
	s.state.mu.Lock()
	s.state.exportsByID[exportPath] = entry
	s.state.exportsByVH[vHash] = append(s.state.exportsByVH[vHash], entry)
	s.state.mu.Unlock()
	return capCfg, nil
}

// ReleaseExport revokes all capability exports for viewID and invalidates all handles and cached listings.
func (s *Server) ReleaseExport(viewID ViewID) {
	vHash, viewCore := s.handles.ViewHash(viewID), coreForView(s, viewID)
	s.state.mu.Lock()
	entries := s.state.exportsByVH[vHash]
	delete(s.state.exportsByVH, vHash)
	for _, e := range entries {
		delete(s.state.exportsByID, e.cap.ExportPath)
	}
	s.state.mu.Unlock()
	s.gate.releaseView(vHash, entries)
	s.stability.ReleaseView(vHash)
	s.stability.sidecars.releaseCore(viewCore)
	s.dirs.ReleaseView(vHash)
	s.handles.ReleaseView(viewCore, viewID)
}

// FlushForCheckpoint flushes and syncs all pending UNSTABLE writes for viewID and
// snapshots the Core journal so a checkpoint barrier captures durable state.
func (s *Server) FlushForCheckpoint(ctx context.Context, viewID ViewID) error {
	viewCore := coreForView(s, viewID)
	if err := s.stability.FlushView(ctx, viewCore, s.handles, s.cfg.FaultInjector, viewID); err != nil {
		return err
	}
	_, err := viewCore.SnapshotJournal()
	return err
}

// IsAlive reports whether the server is still accepting and serving requests.
func (s *Server) IsAlive() bool { return !s.state.closed.Load() }

// ResetUnstableState discards uncommitted UNSTABLE write buffers and rotates the 8-byte
// write verifier to simulate a server restart between UNSTABLE writes and COMMIT.
func (s *Server) ResetUnstableState() [8]byte { return s.stability.ResetUnstableState() }

// WriteVerifier returns the server's current 8-byte NFSv3 write verifier.
func (s *Server) WriteVerifier() [8]byte { return s.stability.WriteVerifier() }

// HandleCount returns the current number of cached handles in the bounded handle table.
func (s *Server) HandleCount() int { return s.handles.Count() }

// ActiveConnections returns the current number of admitted client TCP connections.
func (s *Server) ActiveConnections() int {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	return len(s.state.conns)
}

// ResolveHandleForView resolves a raw wire handle in O(1) and verifies it belongs to expectedView.
func (s *Server) ResolveHandleForView(raw []byte, expectedView ViewID) (layeredfs.QualifiedPath, error) {
	var expHash uint64
	if expectedView != "" {
		expHash = s.handles.ViewHash(expectedView)
	}
	rec, err := s.handles.Resolve(raw, expHash)
	if err != nil {
		return layeredfs.QualifiedPath{}, err
	}
	return rec.qpath, nil
}

// Close stops accepting connections, closes active client sockets, and releases resources.
func (s *Server) Close() error {
	if !s.state.closed.CompareAndSwap(false, true) {
		return nil
	}
	err := s.listener.Close()
	s.state.mu.Lock()
	var views []ViewID
	for _, list := range s.state.exportsByVH {
		for _, e := range list {
			views = append(views, e.cap.ViewID)
		}
	}
	for c := range s.state.conns {
		_ = c.Close()
	}
	s.state.mu.Unlock()
	close(s.stopped)
	s.state.wg.Wait()
	for _, v := range views {
		s.handles.ReleaseView(coreForView(s, v), v)
	}
	s.gate.closeAll()
	return err
}

func runDispatchLoop(s *Server) {
	defer s.state.wg.Done()
	for {
		select {
		case <-s.stopped:
			return
		case fn := <-s.queue:
			select {
			case <-s.stopped:
				return
			case s.workSem <- struct{}{}:
			}
			s.state.wg.Add(1)
			go func(task func()) {
				defer func() { <-s.workSem; s.state.wg.Done() }()
				task()
			}(fn)
		}
	}
}

func runAcceptLoop(s *Server) {
	defer s.state.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.state.closed.Load() {
				return
			}
			// Descriptor exhaustion makes Accept fail immediately and repeatedly.
			select {
			case <-s.stopped:
				return
			case <-time.After(acceptRetryDelay):
			}
			continue
		}
		if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); !ok || !tcpAddr.IP.IsLoopback() {
			_ = conn.Close()
			continue
		}
		s.state.mu.Lock()
		if s.state.closed.Load() || len(s.state.conns) >= s.cfg.MaxConnections {
			s.state.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.state.conns[conn] = struct{}{}
		s.state.mu.Unlock()
		s.state.wg.Add(1)
		go func(c net.Conn) {
			defer func() {
				s.state.mu.Lock()
				delete(s.state.conns, c)
				s.state.mu.Unlock()
				_ = c.Close()
				s.state.wg.Done()
			}()
			serveConn(s, c)
		}(conn)
	}
}
