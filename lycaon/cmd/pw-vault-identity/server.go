package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

// Peer is what the kernel says about a connecting process.
type Peer struct {
	UID int
	PID int
}

const requestDeadline = 30 * time.Second

// Server answers identity requests for verified engines.
type Server struct {
	Store    *Store
	Verifier launchverify.Verifier
	// Manifest is loaded per connection, so a package upgrade applies to the
	// next engine without restarting the service.
	Manifest    string
	TrustPolicy string
	PeerOf      func(net.Conn) (Peer, error)
	// IdleExit ends Serve after this long without a connection; the socket
	// unit starts the service again on demand. Zero serves until ctx ends.
	IdleExit time.Duration
	Log      *slog.Logger

	active atomic.Int64
	wg     sync.WaitGroup
}

// Serve accepts connections until ctx ends or the service has been idle.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := make(chan struct{})
	if s.IdleExit > 0 {
		go s.watchIdle(ctx, idle)
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-idle:
		}
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			s.wg.Wait()
			if ctx.Err() != nil || isIdleClosed(idle) {
				return nil
			}
			return err
		}
		s.active.Add(1)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.active.Add(-1)
			s.serveConn(ctx, conn)
		}()
	}
}

func isIdleClosed(idle chan struct{}) bool {
	select {
	case <-idle:
		return true
	default:
		return false
	}
}

func (s *Server) watchIdle(ctx context.Context, idle chan struct{}) {
	ticker := time.NewTicker(s.IdleExit / 4)
	defer ticker.Stop()
	quietSince := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if s.active.Load() > 0 {
				quietSince = now
				continue
			}
			if now.Sub(quietSince) >= s.IdleExit {
				close(idle)
				return
			}
		}
	}
}

// connection is one verified caller.
type connection struct {
	peer  Peer
	chain launchverify.Chain
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stopClosing := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClosing()
	peer, err := s.PeerOf(conn)
	if err != nil {
		s.Log.Warn("vault identity peer credentials unavailable", "error", err)
		return
	}
	verifiers, resp := s.verifiersFor()
	if resp != nil {
		s.answer(conn, *resp)
		return
	}
	var chain launchverify.Chain
	var verifier launchverify.Verifier
	for _, candidate := range verifiers {
		chain, err = candidate.VerifyChain(peer.PID, peer.UID)
		if err == nil {
			verifier = candidate
			break
		}
		if launchverify.CodeOf(err) != launchverify.CodeExecutableMismatch {
			break
		}
	}
	if err != nil {
		s.Log.Warn("vault identity caller refused", "uid", peer.UID, "pid", peer.PID, "code", launchverify.CodeOf(err), "error", err)
		s.answer(conn, refusal(err))
		return
	}
	caller := connection{peer: peer, chain: chain}
	for {
		_ = conn.SetDeadline(time.Now().Add(requestDeadline))
		var req vaultidentity.Request
		if err := vaultidentity.ReadFrame(conn, &req); err != nil {
			return
		}
		// The verified process must still be the one connected.
		if err := verifier.Recheck(chain); err != nil {
			s.Log.Warn("vault identity caller changed", "uid", peer.UID, "pid", peer.PID, "code", launchverify.CodeOf(err))
			s.answer(conn, refusal(err))
			return
		}
		resp := s.handle(caller, req)
		// Accounts are configuration-root digests, never secrets.
		s.Log.Info("vault identity request", "op", req.Op, "account", req.Account, "status", resp.Status, "uid", peer.UID, "pid", peer.PID)
		if !s.answer(conn, resp) {
			return
		}
	}
}

// verifiersFor loads explicit trust for one connection; policy changes restart
// the NixOS unit, terminating connections that used its previous policy.
func (s *Server) verifiersFor() ([]launchverify.Verifier, *vaultidentity.Response) {
	if s.TrustPolicy != "" {
		verifiers, err := loadTrustPolicy(s.TrustPolicy, s.Verifier)
		if err == nil {
			return verifiers, nil
		}
		s.Log.Error("vault identity trust policy unreadable", "error", err)
		return nil, &vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Code: string(launchverify.CodeManifestUnreadable), Message: "the administrator-selected custody policy is unavailable"}
	}
	manifest, err := pkgmanifest.Load(s.Manifest)
	if err != nil {
		s.Log.Error("vault identity manifest unreadable", "path", s.Manifest, "error", err)
		return nil, &vaultidentity.Response{
			Status: vaultidentity.StatusUnavailable, Code: string(launchverify.CodeManifestUnreadable),
			Message: "the installed executable manifest cannot be read",
		}
	}
	verifier := s.Verifier
	verifier.Manifest = manifest
	return []launchverify.Verifier{verifier}, nil
}

func refusal(err error) vaultidentity.Response {
	code := launchverify.CodeOf(err)
	status := vaultidentity.StatusRefused
	if code == launchverify.CodeManifestUnreadable {
		status = vaultidentity.StatusUnavailable
	}
	return vaultidentity.Response{Status: status, Code: string(code), Message: err.Error()}
}

func (s *Server) answer(conn net.Conn, resp vaultidentity.Response) bool {
	return vaultidentity.WriteFrame(conn, resp) == nil
}

func (s *Server) handle(caller connection, req vaultidentity.Request) vaultidentity.Response {
	ok := vaultidentity.Response{Status: vaultidentity.StatusOK, LauncherPID: caller.chain.Launcher.PID}
	if req.Op == vaultidentity.OpProbe {
		return ok
	}
	if !vaultidentity.ValidAccount(req.Account) {
		return vaultidentity.Response{Status: vaultidentity.StatusInvalid, Code: "account", Message: "the account name is invalid"}
	}
	if len(req.Value) > vaultidentity.MaxValueBytes {
		return vaultidentity.Response{Status: vaultidentity.StatusInvalid, Code: "value", Message: "the identity is too large"}
	}
	var err error
	switch req.Op {
	case vaultidentity.OpRead:
		ok.Value, err = s.Store.Read(caller.peer.UID, req.Account)
	case vaultidentity.OpCreate:
		err = s.Store.Create(caller.peer.UID, req.Account, req.Value)
	case vaultidentity.OpRemove:
		err = s.Store.Remove(caller.peer.UID, req.Account)
	default:
		return vaultidentity.Response{Status: vaultidentity.StatusInvalid, Code: "op", Message: "unknown operation"}
	}
	switch {
	case err == nil:
		return ok
	case errors.Is(err, errNotFound):
		return vaultidentity.Response{Status: vaultidentity.StatusNotFound, LauncherPID: ok.LauncherPID}
	case errors.Is(err, errExists):
		return vaultidentity.Response{Status: vaultidentity.StatusExists, LauncherPID: ok.LauncherPID}
	default:
		s.Log.Error("vault identity store failed", "op", req.Op, "uid", caller.peer.UID, "error", err)
		return vaultidentity.Response{Status: vaultidentity.StatusUnavailable, Code: "store", Message: "the identity store failed"}
	}
}
