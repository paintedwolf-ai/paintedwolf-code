//go:build linux

package linuxsandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The performer creates, links, and moves names for the sandbox. The inner
// helper starts it after the Landlock ruleset applies and before the seccomp
// filter does, so it holds the sandbox's mount namespace, credentials, and
// Landlock domain without passing its own calls back to the supervisor. The
// supervisor sends it each name-creating call it was notified of, with its own
// copy of every path and the caller's directories, and it resolves the parent,
// decides the name against the floors, and performs the call on the parent it
// resolved. Nothing the caller changes afterwards reaches the kernel.

// PerformerFlag, as a binary's first argument, starts it as the sandbox's
// performer; the binary that carries the confinement helper dispatches it.
const PerformerFlag = "--create-performer"

// performerFD is the performer's end of its socket to the supervisor.
const performerFD = 3

// pathCall is one notified call, as the supervisor read it.
type pathCall struct {
	Op string `json:"op"`
	// Path is the name the call creates; for link and rename, the existing one.
	Path string `json:"path"`
	// NewPath is the name link and rename create.
	NewPath string `json:"new_path,omitempty"`
	// Content is a symlink's target, which names nothing until it is followed.
	Content string `json:"content,omitempty"`
	Flags   uint64 `json:"flags,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
	Dev     uint64 `json:"dev,omitempty"`
	Resolve uint64 `json:"resolve,omitempty"`
	Umask   uint32 `json:"umask"`
	// Dirs reports which directories accompany the call, in order: the one
	// Path resolves from, then the one NewPath resolves from.
	Dirs int `json:"dirs"`
	// Absolute marks, by position, a name whose absolute path makes its
	// directory irrelevant; no descriptor travels for it.
	Absolute uint8 `json:"absolute,omitempty"`
}

// slots places the received directories at the positions of the call's
// names, the working directory standing for an absolute name's.
func (c pathCall) slots(received []int) ([]int, bool) {
	var out []int
	next := 0
	for i := 0; i < c.Dirs+bitsSet(c.Absolute); i++ {
		if c.Absolute&(1<<i) != 0 {
			out = append(out, unix.AT_FDCWD)
			continue
		}
		if next >= len(received) {
			return nil, false
		}
		out = append(out, received[next])
		next++
	}
	return out, next == len(received)
}

func bitsSet(v uint8) int {
	n := 0
	for ; v != 0; v &= v - 1 {
		n++
	}
	return n
}

// Calls the performer serves.
const (
	callOpen    = "open"
	callMkdir   = "mkdir"
	callMknod   = "mknod"
	callSymlink = "symlink"
	callLink    = "link"
	callRename  = "rename"
)

// pathResult is the performer's answer; an opened descriptor travels with it.
type pathResult struct {
	Errno int `json:"errno,omitempty"`
	// Refusal is set when a floor refused the call.
	Refusal *Refusal `json:"refusal,omitempty"`
}

// maxPathMessage bounds one request: two paths, a symlink target, and fields.
const maxPathMessage = 3*unix.PathMax + 1024

// startPerformer runs the performer on the restricted thread, gives it the
// floors, and hands its socket to the supervisor.
func startPerformer(fs Filesystem) error {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("performer socket: %w", err)
	}
	defer func() { _ = unix.Close(pair[0]) }()
	floors, err := json.Marshal(fs)
	if err != nil {
		_ = unix.Close(pair[1])
		return err
	}
	devnull, err := unix.Open("/dev/null", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(pair[1])
		return fmt.Errorf("performer stdio: %w", err)
	}
	_, err = syscall.ForkExec("/proc/self/exe", []string{"pw-confine-performer", PerformerFlag}, &syscall.ProcAttr{
		Env:   []string{},
		Files: []uintptr{uintptr(devnull), uintptr(devnull), 2, uintptr(pair[1])},
		Sys:   &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL},
	})
	_ = unix.Close(devnull)
	_ = unix.Close(pair[1])
	if err != nil {
		return fmt.Errorf("start performer: %w", err)
	}
	if err := unix.Sendmsg(pair[0], floors, nil, nil, 0); err != nil {
		return fmt.Errorf("performer floors: %w", err)
	}
	if err := sendHandoff(handoff{Kind: handoffPerformer}, pair[0]); err != nil {
		return fmt.Errorf("hand off performer: %w", err)
	}
	return nil
}

// ServePerformer is the performer process: it sheds the capabilities the
// inner helper kept, becomes non-dumpable so no sandbox process can read or
// steer it, and answers the supervisor until it closes the socket.
func ServePerformer() error {
	runtime.LockOSThread()
	// The caller's umask applies to each call; this thread keeps its own.
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return fmt.Errorf("performer umask: %w", err)
	}
	if err := dropCapabilities(); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("performer dumpable: %w", err)
	}
	return servePathCalls(performerFD)
}

// servePathCalls reads the floors, then answers calls on fd until the
// supervisor closes it.
func servePathCalls(fd int) error {
	buf := make([]byte, maxPathMessage)
	n, _, _, _, err := unix.Recvmsg(fd, buf, nil, 0)
	if err != nil {
		return fmt.Errorf("performer floors: %w", err)
	}
	var fs Filesystem
	if err := json.Unmarshal(buf[:n], &fs); err != nil {
		return fmt.Errorf("performer floors: %w", err)
	}
	floors := newCreationFloors(fs)
	oob := make([]byte, unix.CmsgSpace(4*2))
	for {
		n, oobn, _, _, err := unix.Recvmsg(fd, buf, oob, unix.MSG_CMSG_CLOEXEC)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || (n == 0 && oobn == 0) {
			return nil
		}
		dirs := receivedFDs(oob[:oobn])
		var call pathCall
		result, opened := pathResult{Errno: int(unix.EINVAL)}, -1
		if json.Unmarshal(buf[:n], &call) == nil && call.Dirs == len(dirs) {
			if slots, ok := call.slots(dirs); ok {
				result, opened = floors.perform(call, slots)
			}
		}
		for _, dir := range dirs {
			_ = unix.Close(dir)
		}
		reply, _ := json.Marshal(result)
		var rights []byte
		if opened >= 0 {
			rights = unix.UnixRights(opened)
		}
		err = unix.Sendmsg(fd, reply, rights, nil, 0)
		if opened >= 0 {
			_ = unix.Close(opened)
		}
		if err != nil {
			return nil
		}
	}
}

func receivedFDs(oob []byte) []int {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	var out []int
	for i := range msgs {
		fds, err := unix.ParseUnixRights(&msgs[i])
		if err == nil {
			out = append(out, fds...)
		}
	}
	return out
}

// pathCallTimeout bounds one performed call; a slow filesystem can take long.
const pathCallTimeout = 60 * time.Second

// performerClient is the supervisor's side of the performer's socket. Calls
// run one at a time, so each answer belongs to the request before it.
type performerClient struct {
	mu sync.Mutex
	fd int
	// broken is set once an answer went missing.
	broken bool
}

// newPerformerClient bounds the wait for each answer on fd.
func newPerformerClient(fd int) *performerClient {
	tv := unix.NsecToTimeval(pathCallTimeout.Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	return &performerClient{fd: fd}
}

func (c *performerClient) call(call pathCall, dirs []int) (pathResult, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return pathResult{}, -1, errors.New("the sandbox performer stopped answering")
	}
	raw, err := json.Marshal(call)
	if err != nil {
		return pathResult{}, -1, err
	}
	var rights []byte
	if len(dirs) > 0 {
		rights = unix.UnixRights(dirs...)
	}
	err = unix.Sendmsg(c.fd, raw, rights, nil, 0)
	for errors.Is(err, unix.EINTR) {
		err = unix.Sendmsg(c.fd, raw, rights, nil, 0)
	}
	if err != nil {
		c.broken = true
		return pathResult{}, -1, err
	}
	buf := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(4))
	n, oobn, _, _, err := unix.Recvmsg(c.fd, buf, oob, unix.MSG_CMSG_CLOEXEC)
	// A receive timeout makes a signal, such as the runtime's preemption,
	// interrupt the wait instead of restarting it.
	for errors.Is(err, unix.EINTR) {
		n, oobn, _, _, err = unix.Recvmsg(c.fd, buf, oob, unix.MSG_CMSG_CLOEXEC)
	}
	if err != nil || n == 0 {
		c.broken = true
		return pathResult{}, -1, errors.New("no answer from the sandbox performer")
	}
	opened := -1
	if fds := receivedFDs(oob[:oobn]); len(fds) == 1 {
		opened = fds[0]
	} else {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}
	var result pathResult
	if err := json.Unmarshal(buf[:n], &result); err != nil {
		if opened >= 0 {
			_ = unix.Close(opened)
		}
		c.broken = true
		return pathResult{}, -1, err
	}
	return result, opened, nil
}

// fdCloser closes a raw descriptor with the supervisor.
type fdCloser int

func (f fdCloser) Close() error { return unix.Close(int(f)) }
