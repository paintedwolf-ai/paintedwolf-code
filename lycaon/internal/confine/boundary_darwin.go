//go:build darwin

package confine

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/lycaon/lycaon/internal/fspath"
)

const socketRelayHeaderPrefix = "; socket-relay: "

type socketRelayLease struct {
	dir       string
	listeners []net.Listener
	sessionID string
	once      sync.Once
	mu        sync.Mutex
	feed      *refusalFeed
	firstErr  *SocketResolveError
	conns     map[io.Closer]struct{}
}

var (
	socketRelayMu       sync.Mutex
	activeSessionRelays = map[string][]*socketRelayLease{}
	pendingCmdRelays    = map[string]*socketRelayLease{}
)

func (l *socketRelayLease) bindFeed(f *refusalFeed) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.feed = f
	l.mu.Unlock()
}

func (l *socketRelayLease) recordRefusal(g SocketGrant) {
	rej := &SocketResolveError{Code: SocketResolveChanged, Path: g.ApprovedPath, Err: ErrSocketListenerChanged}
	recordSocketRelayRefusal(g, rej)
	l.mu.Lock()
	if l.firstErr == nil {
		l.firstErr = rej
	}
	feed := l.feed
	l.mu.Unlock()
	if feed != nil {
		feed.record(kernelRefusal{Process: "socket-relay", Operation: "network-outbound", Target: g.ApprovedPath, Count: 1})
	}
}

func (l *socketRelayLease) refusal() *SocketResolveError {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.firstErr
}

func (l *socketRelayLease) trackConn(c io.Closer) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conns == nil {
		return false
	}
	l.conns[c] = struct{}{}
	return true
}

func (l *socketRelayLease) untrackConn(c io.Closer) {
	l.mu.Lock()
	delete(l.conns, c)
	l.mu.Unlock()
}

func (l *socketRelayLease) close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		for _, ln := range l.listeners {
			_ = ln.Close()
		}
		l.mu.Lock()
		conns := l.conns
		l.conns = nil
		l.mu.Unlock()
		for c := range conns {
			_ = c.Close()
		}
		_ = os.RemoveAll(l.dir)
		if l.sessionID != "" {
			socketRelayMu.Lock()
			list := activeSessionRelays[l.sessionID]
			for i, item := range list {
				if item == l {
					activeSessionRelays[l.sessionID] = append(list[:i], list[i+1:]...)
					break
				}
			}
			if len(activeSessionRelays[l.sessionID]) == 0 {
				delete(activeSessionRelays, l.sessionID)
			}
			socketRelayMu.Unlock()
		}
	})
}

// ForgetSocketSession closes any live socket relays associated with sessionID.
func ForgetSocketSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	socketRelayMu.Lock()
	relays := append([]*socketRelayLease(nil), activeSessionRelays[sessionID]...)
	delete(activeSessionRelays, sessionID)
	socketRelayMu.Unlock()
	for _, r := range relays {
		r.close()
	}
}

func newRelayDir() (string, error) {
	base := "/var/tmp"
	if acct, err := user.LookupId(strconv.Itoa(os.Getuid())); err == nil && acct != nil && acct.HomeDir != "" && len(acct.HomeDir)+28 <= maxSocketPathBytes {
		base = acct.HomeDir
	}
	dir, err := os.MkdirTemp(base, ".pw-sr-")
	if err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)
	if canon := fspath.CanonicalPath(dir); canon != "" {
		dir = canon
	}
	return dir, nil
}

func openActionSocketRelays(c *Confinement, sessionID string) (*socketRelayLease, error) {
	if c == nil || c.HostExecution || len(c.SocketGrants) == 0 {
		return nil, nil
	}
	dir, err := newRelayDir()
	if err != nil {
		return nil, fmt.Errorf("create socket relay dir: %w", err)
	}
	lease := &socketRelayLease{dir: dir, sessionID: strings.TrimSpace(sessionID), conns: map[io.Closer]struct{}{}}
	for i := range c.SocketGrants {
		g := c.SocketGrants[i]
		if g.RelayPath != "" || socketPathSyntaxReason(g.ApprovedPath) != "" || socketPathSyntaxReason(g.ResolvedPath) != "" {
			continue
		}
		if err := RevalidateSocketGrant(g); err != nil {
			lease.recordRefusal(g)
			continue
		}
		g.Listener = effectiveSocketListener(g)
		relayPath := filepath.Join(dir, fmt.Sprintf("s%d.sock", i))
		ln, listenErr := net.ListenUnix("unix", &net.UnixAddr{Name: relayPath, Net: "unix"})
		if listenErr != nil {
			lease.close()
			return nil, fmt.Errorf("listen socket relay: %w", listenErr)
		}
		_ = os.Chmod(relayPath, 0o600)
		g.RelayPath = relayPath
		c.SocketGrants[i] = g
		lease.listeners = append(lease.listeners, ln)
		go serveSocketRelay(lease, ln, g)
	}
	if lease.sessionID != "" {
		socketRelayMu.Lock()
		activeSessionRelays[lease.sessionID] = append(activeSessionRelays[lease.sessionID], lease)
		socketRelayMu.Unlock()
	}
	return lease, nil
}

func serveSocketRelay(lease *socketRelayLease, ln net.Listener, g SocketGrant) {
	for {
		clientConn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleRelayConn(lease, ln, clientConn, g)
	}
}

func handleRelayConn(lease *socketRelayLease, ln net.Listener, clientConn net.Conn, g SocketGrant) {
	if !lease.trackConn(clientConn) {
		_ = clientConn.Close()
		return
	}
	defer func() {
		lease.untrackConn(clientConn)
		_ = clientConn.Close()
	}()
	refuse := func() {
		lease.recordRefusal(g)
		_ = ln.Close()
	}
	if preDialSocketCheck(g) != nil {
		refuse()
		return
	}
	unixConn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: g.ResolvedPath, Net: "unix"})
	if err != nil || !lease.trackConn(unixConn) {
		return
	}
	defer func() {
		lease.untrackConn(unixConn)
		_ = unixConn.Close()
	}()
	fiAfter, err := os.Lstat(g.ResolvedPath)
	if err != nil || fiAfter.Mode()&os.ModeSocket == 0 || socketHardLinked(fiAfter) || verifyConnectedSocketPeer(unixConn, fiAfter, g) != nil {
		refuse()
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(unixConn, clientConn)
		_ = unixConn.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(clientConn, unixConn)
		if uc, ok := clientConn.(*net.UnixConn); ok {
			_ = uc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

func pendingRelayKey(c Confinement) string {
	if c.refusalTag != "" {
		return "tag:" + c.refusalTag
	}
	var parts []string
	for _, g := range c.SocketGrants {
		parts = append(parts, g.ApprovedPath+"="+g.ResolvedPath)
	}
	return "grants:" + strings.Join(parts, "|")
}

func claimCommandSocketRelays(c Confinement) func() {
	key := pendingRelayKey(c)
	socketRelayMu.Lock()
	lease := pendingCmdRelays[key]
	delete(pendingCmdRelays, key)
	socketRelayMu.Unlock()
	if lease == nil {
		return func() {}
	}
	return lease.close
}

// renderProfile is the Seatbelt profile on this platform.
func renderProfile(c Confinement) (string, error) {
	needRelay := false
	for _, g := range c.SocketGrants {
		if g.RelayPath == "" {
			needRelay = true
			break
		}
	}
	var cmdLease *socketRelayLease
	if needRelay {
		key := pendingRelayKey(c)
		lease, err := openActionSocketRelays(&c, "")
		if err != nil {
			return "", err
		}
		lease.bindFeed(refusalWatch.feedFor(c.refusalTag))
		cmdLease = lease
		socketRelayMu.Lock()
		pendingCmdRelays[key].close()
		pendingCmdRelays[key] = lease
		socketRelayMu.Unlock()
	}
	profile, err := renderSeatbeltProfile(c)
	if err != nil {
		cmdLease.close()
		return "", err
	}
	var meta strings.Builder
	for _, g := range c.SocketGrants {
		if g.RelayPath != "" {
			meta.WriteString(socketRelayHeaderPrefix + g.ApprovedPath + "\t" + g.ResolvedPath + "\t" + g.RelayPath + "\n")
		}
	}
	return profile + meta.String(), nil
}

// applyAndExec applies the profile to this process, confirms kernel membership,
// and replaces the process with the target.
func applyAndExec(profile []byte, resolved string, target []string) error {
	raw := string(profile)
	if err := applySeatbelt(raw); err != nil {
		return err
	}
	if err := attestConfined(); err != nil {
		return err
	}
	mappedTarget, mappedEnv := applySocketRelaysToExec(raw, target, os.Environ())
	if err := syscall.Exec(resolved, mappedTarget, mappedEnv); err != nil {
		return ClassifyExecError(resolved, err)
	}
	return nil
}
