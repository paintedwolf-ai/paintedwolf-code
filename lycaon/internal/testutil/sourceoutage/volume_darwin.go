//go:build darwin

package sourceoutage

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

// Mode is how the kernel treats a call the outage interrupts.
type Mode int

const (
	// Soft mounts fail an interrupted call once the client gives up.
	Soft Mode = iota
	// Hard mounts hold an interrupted call until the transport returns.
	Hard
)

// Every lookup and attribute read reaches the server, so an outage is seen at
// the next access rather than hidden by the client's caches.
const uncachedOptions = "acregmin=0,acregmax=0,acdirmin=0,acdirmax=0,nonegnamecache"

// Volume is a directory the kernel's NFS client mounts from a loopback
// server through a relay the test controls.
type Volume struct {
	// Dir is the mounted directory.
	Dir string
	// Control is the socket helper processes reach through EnvControl.
	Control string
	relay   *relay
}

// Mount serves a fresh directory and mounts it with the user's own
// mount_nfs, as the mounted worker adapter does; unmounting is a cleanup.
func Mount(t *testing.T, mode Mode) *Volume {
	t.Helper()
	backing := t.TempDir()
	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the NFS server: %v", err)
	}
	served := changeFS{Filesystem: osfs.New(backing), root: backing}
	handler := nfshelper.NewCachingHandler(nfshelper.NewNullAuthHandler(served), 4096)
	go func() { _ = nfs.Serve(server, handler) }()
	r, err := newRelay(server.Addr().String())
	if err != nil {
		_ = server.Close()
		t.Fatalf("start the relay: %v", err)
	}
	port := r.listener.Addr().(*net.TCPAddr).Port
	options := fmt.Sprintf("vers=3,tcp,port=%d,mountport=%d,locallocks,nodev,nosuid,nobrowse,%s", port, port, uncachedOptions)
	if mode == Soft {
		options += ",soft,timeo=10,retrans=2"
	}
	dir := t.TempDir()
	if out, err := exec.Command("/sbin/mount_nfs", "-o", options, "127.0.0.1:/", dir).CombinedOutput(); err != nil {
		r.close()
		_ = server.Close()
		t.Fatalf("mount_nfs: %v: %s", err, out)
	}
	control, err := os.MkdirTemp("", "outage")
	if err != nil {
		t.Fatalf("control directory: %v", err)
	}
	volume := &Volume{Dir: dir, Control: filepath.Join(control, "c"), relay: r}
	commands, err := net.Listen("unix", volume.Control)
	if err != nil {
		t.Fatalf("control socket: %v", err)
	}
	go volume.serve(commands)
	t.Cleanup(func() {
		_ = commands.Close()
		r.restore()
		if out, err := exec.Command("/sbin/umount", "-f", dir).CombinedOutput(); err != nil {
			t.Errorf("unmount %s: %v: %s", dir, err, out)
		}
		r.close()
		_ = server.Close()
		_ = os.RemoveAll(control)
	})
	return volume
}

// Stop cuts the transport: open connections close, and new ones are held
// with their calls buffered.
func (v *Volume) Stop() { v.relay.stop() }

// Restore forwards every held call and resumes ordinary relaying.
func (v *Volume) Restore() { v.relay.restore() }

// AwaitHeldCall returns once the kernel has sent an NFS call (not a ping)
// that the outage holds.
func (v *Volume) AwaitHeldCall() { <-v.relay.heldCall() }

func (v *Volume) serve(commands net.Listener) {
	for {
		conn, err := commands.Accept()
		if err != nil {
			return
		}
		command, err := bufio.NewReader(conn).ReadString('\n')
		if err == nil {
			switch strings.TrimSpace(command) {
			case "stop":
				v.Stop()
			case "restore":
				v.Restore()
			}
			_, _ = conn.Write([]byte("ok\n"))
		}
		_ = conn.Close()
	}
}

// changeFS lets clients set modes, owners and times on the served files.
type changeFS struct {
	billy.Filesystem
	root string
}

func (f changeFS) Chmod(name string, mode os.FileMode) error { return os.Chmod(f.path(name), mode) }
func (f changeFS) Lchown(name string, uid, gid int) error  { return os.Lchown(f.path(name), uid, gid) }
func (f changeFS) Chown(name string, uid, gid int) error   { return os.Chown(f.path(name), uid, gid) }
func (f changeFS) Chtimes(name string, atime, mtime time.Time) error {
	return os.Chtimes(f.path(name), atime, mtime)
}
func (f changeFS) path(name string) string { return filepath.Join(f.root, name) }

type relay struct {
	server   string
	listener net.Listener
	mu       sync.Mutex
	stopped  bool
	open     []net.Conn
	held     []*heldConn
	call     chan struct{}
	called   bool
}

// heldConn reads whole RPC records while the transport is cut, so they can be
// replayed to the server in order when it returns.
type heldConn struct {
	client  net.Conn
	pending []byte
	server  net.Conn
}

func newRelay(server string) (*relay, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &relay{server: server, listener: listener, call: make(chan struct{})}
	go r.accept()
	return r, nil
}

func (r *relay) accept() {
	for {
		client, err := r.listener.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		if r.stopped {
			h := &heldConn{client: client}
			r.held = append(r.held, h)
			r.mu.Unlock()
			go r.hold(h)
			continue
		}
		r.mu.Unlock()
		r.forward(client)
	}
}

func (r *relay) forward(client net.Conn) {
	server, err := net.Dial("tcp", r.server)
	if err != nil {
		_ = client.Close()
		return
	}
	r.mu.Lock()
	r.open = append(r.open, client, server)
	r.mu.Unlock()
	go pipe(server, client)
	go pipe(client, server)
}

func pipe(to, from net.Conn) {
	_, _ = io.Copy(to, from)
	_ = to.Close()
	_ = from.Close()
}

func (r *relay) hold(h *heldConn) {
	for {
		record, call, err := readRecord(h.client)
		if err != nil {
			_ = h.client.Close()
			return
		}
		r.mu.Lock()
		if h.server != nil {
			_, err = h.server.Write(record)
		} else {
			h.pending = append(h.pending, record...)
			if call && !r.called {
				r.called = true
				close(r.call)
			}
		}
		r.mu.Unlock()
		if err != nil {
			_ = h.client.Close()
			return
		}
	}
}

// readRecord reads one RPC record and reports whether it carries an NFS call
// other than the NULL ping.
func readRecord(conn net.Conn) ([]byte, bool, error) {
	var record []byte
	var call bool
	for first := true; ; first = false {
		var mark [4]byte
		if _, err := io.ReadFull(conn, mark[:]); err != nil {
			return nil, false, err
		}
		header := binary.BigEndian.Uint32(mark[:])
		fragment := make([]byte, header&0x7fffffff)
		if _, err := io.ReadFull(conn, fragment); err != nil {
			return nil, false, err
		}
		if first && len(fragment) >= 24 {
			call = binary.BigEndian.Uint32(fragment[4:8]) == 0 &&
				binary.BigEndian.Uint32(fragment[12:16]) == 100003 &&
				binary.BigEndian.Uint32(fragment[20:24]) != 0
		}
		record = append(append(record, mark[:]...), fragment...)
		if header&0x80000000 != 0 {
			return record, call, nil
		}
	}
}

func (r *relay) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	r.call, r.called = make(chan struct{}), false
	for _, conn := range r.open {
		_ = conn.Close()
	}
	r.open = nil
}

func (r *relay) heldCall() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.call
}

func (r *relay) restore() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = false
	for _, h := range r.held {
		server, err := net.Dial("tcp", r.server)
		if err != nil {
			_ = h.client.Close()
			continue
		}
		if _, err := server.Write(h.pending); err != nil {
			_ = h.client.Close()
			_ = server.Close()
			continue
		}
		h.server, h.pending = server, nil
		r.open = append(r.open, h.client, server)
		go pipe(h.client, server)
	}
	r.held = nil
}

func (r *relay) close() {
	_ = r.listener.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, conn := range append(r.open, heldClients(r.held)...) {
		_ = conn.Close()
	}
}

func heldClients(held []*heldConn) []net.Conn {
	conns := make([]net.Conn, 0, len(held))
	for _, h := range held {
		conns = append(conns, h.client)
	}
	return conns
}
