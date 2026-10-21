//go:build linux

// protecteddesktop stands in for the desktop image in protected launch tests.
// It reports the kernel facts it was started with, deliberately stays
// dumpable so only the namespace boundary can refuse an outside reader, and
// waits for its standard input to close.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchguard"
	"github.com/lycaon/lycaon/internal/launchverify"
)

// Facts mirrors the test's decoding of this report.
type Facts struct {
	PID             int                         `json:"pid"`
	UIDs            [3]int                      `json:"uids"`
	GIDs            [3]int                      `json:"gids"`
	Groups          []int                       `json:"groups"`
	Namespace       launchverify.NamespaceFacts `json:"namespace"`
	Executable      string                      `json:"executable"`
	Arguments       []string                    `json:"arguments"`
	Environment     []string                    `json:"environment_keys"`
	BrokerPeerPID   int                         `json:"broker_peer_pid"`
	BrokerPeerUID   int                         `json:"broker_peer_uid"`
	BrokerType      int                         `json:"broker_type"`
	LifetimeRegular bool                        `json:"lifetime_regular"`
	OpenBeyond      []int                       `json:"open_beyond"`
	InstallWrite    string                      `json:"install_write"`
	Remount         string                      `json:"remount"`
	ReadError       string                      `json:"read_error,omitempty"`
	BrokerOutput    string                      `json:"broker_output,omitempty"`
	BrokerExit      int                         `json:"broker_exit"`
	BrokerError     string                      `json:"broker_error,omitempty"`
	AbandonedEnded  bool                        `json:"abandoned_ended"`
}

func main() {
	var facts Facts
	facts.PID = os.Getpid()
	facts.UIDs[0], facts.UIDs[1], facts.UIDs[2] = unix.Getresuid()
	facts.GIDs[0], facts.GIDs[1], facts.GIDs[2] = unix.Getresgid()
	facts.Groups, _ = unix.Getgroups()
	ns, err := launchverify.ReadNamespace("/proc", os.Getpid())
	if err != nil {
		facts.ReadError = err.Error()
	}
	facts.Namespace = ns
	facts.Executable, _ = os.Readlink("/proc/self/exe")
	facts.Arguments = os.Args
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		facts.Environment = append(facts.Environment, key)
	}
	sort.Strings(facts.Environment)
	if peer, err := unix.GetsockoptUcred(6, unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
		facts.BrokerPeerPID, facts.BrokerPeerUID = int(peer.Pid), int(peer.Uid)
	}
	facts.BrokerType, _ = unix.GetsockoptInt(6, unix.SOL_SOCKET, unix.SO_TYPE)
	var lifetime unix.Stat_t
	facts.LifetimeRegular = unix.Fstat(7, &lifetime) == nil && lifetime.Mode&unix.S_IFMT == unix.S_IFREG
	for fd := 8; fd < 64; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			facts.OpenBeyond = append(facts.OpenBeyond, fd)
		}
	}
	install := filepath.Dir(filepath.Dir(facts.Executable))
	facts.InstallWrite = errnoName(os.WriteFile(filepath.Join(install, "planted"), []byte("x"), 0o644))
	facts.Remount = errnoName(unix.Mount("", install, "", unix.MS_REMOUNT|unix.MS_BIND, ""))
	for _, arg := range os.Args[1:] {
		if arg == "--broker-probe" {
			brokerProbe(&facts)
		}
	}
	// Stay dumpable and traceable by anyone: only the namespace can refuse.
	_ = unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0)
	_ = unix.Prctl(unix.PR_SET_PTRACER, unix.PR_SET_PTRACER_ANY, 0, 0, 0)
	if err := json.NewEncoder(os.Stdout).Encode(facts); err != nil {
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// brokerProbe runs one program through the supervisor's broker and reports
// where it ran, then abandons a second one and reports whether it ended.
func brokerProbe(facts *Facts) {
	broker := os.NewFile(6, "broker")
	null, err := os.Open("/dev/null")
	if err != nil {
		facts.BrokerError = err.Error()
		return
	}
	defer func() { _ = null.Close() }()
	read, write, err := os.Pipe()
	if err != nil {
		facts.BrokerError = err.Error()
		return
	}
	channel, err := launchguard.SendBrokerRequest(broker, launchguard.BrokerRequest{
		Argv:        []string{"sh", "-c", "readlink /proc/self/ns/user; grep NoNewPrivs /proc/self/status"},
		Environment: []string{"PATH=/usr/bin:/bin"}, Dir: "/",
	}, null, write, write)
	_ = write.Close()
	if err != nil {
		facts.BrokerError = err.Error()
		return
	}
	output, _ := io.ReadAll(read)
	facts.BrokerOutput = string(output)
	for {
		event, err := launchguard.ReadBrokerEvent(channel)
		if err != nil {
			facts.BrokerError = err.Error()
			break
		}
		if event.Error != "" {
			facts.BrokerError = event.Error
			break
		}
		if event.Exited {
			facts.BrokerExit = event.Code
			break
		}
	}
	_ = channel.Close()
	sleeper, err := launchguard.SendBrokerRequest(broker, launchguard.BrokerRequest{
		Argv: []string{"sleep", "60"}, Environment: []string{"PATH=/usr/bin:/bin"}, Dir: "/",
	}, null, null, null)
	if err != nil {
		facts.BrokerError = err.Error()
		return
	}
	started, err := launchguard.ReadBrokerEvent(sleeper)
	_ = sleeper.Close()
	if err != nil || started.Started <= 0 {
		facts.BrokerError = "abandoned call never started"
		return
	}
	for i := 0; i < 250; i++ {
		if _, err := os.Stat("/proc/" + strconv.Itoa(started.Started)); err != nil {
			facts.AbandonedEnded = true
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func errnoName(err error) string {
	var errno unix.Errno
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &errno):
		return unix.ErrnoName(errno)
	default:
		return err.Error()
	}
}
