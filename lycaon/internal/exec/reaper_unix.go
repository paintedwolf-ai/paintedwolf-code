//go:build unix

package exec

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/installbinding"
	"github.com/lycaon/lycaon/internal/osprocess"
)

// reaperWriteTimeout bounds a registration so a stalled companion never blocks a launch.
const reaperWriteTimeout = time.Second

var reaper struct {
	mu   sync.Mutex
	pipe *os.File
	cmd  *exec.Cmd
}

func init() {
	if len(os.Args) > 1 && os.Args[1] == ReaperCommand {
		os.Exit(RunReaper(os.Stdin))
	}
}

// StartReaper launches the companion that kills every tracked child once this
// process exits by any path, SIGKILL included; macOS has no parent-death signal.
// Only this process holds the pipe's close-on-exec write end, so the kernel
// closing it at exit is the trigger. The companion leads its own process group,
// out of reach of signals sent to the engine's group.
func StartReaper(name string, args ...string) error {
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	return startReaperLocked(name, args...)
}

func ensureReaper() error {
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	if reaper.pipe != nil {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("reaper executable: %w", err)
	}
	return startReaperLocked(self, ReaperCommand)
}

func startReaperLocked(name string, args ...string) error {
	if err := installbinding.Verify(name); err != nil {
		return err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("reaper pipe: %w", err)
	}
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // G204 — the engine's own executable; the reaper outlives every request
	cmd.Stdin = read
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = read.Close()
		_ = write.Close()
		return fmt.Errorf("start reaper: %w", err)
	}
	_ = read.Close()

	previous := reaper.pipe
	previousCmd := reaper.cmd
	reaper.pipe = write
	reaper.cmd = cmd
	if previous != nil {
		_ = previous.Close()
		if previousCmd != nil {
			_ = previousCmd.Wait()
		}
	}
	return nil
}

func track(kind byte, payload string) func() {
	return trackWithRemovePayload(kind, payload, payload)
}

func trackWithRemovePayload(kind byte, addPayload, removePayload string) func() {
	if !sendReaper('+', kind, addPayload) {
		return func() {}
	}
	var once sync.Once
	return func() { once.Do(func() { sendReaper('-', kind, removePayload) }) }
}

func sendReaper(op, kind byte, payload string) bool {
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	if reaper.pipe == nil {
		return false
	}
	_ = reaper.pipe.SetWriteDeadline(time.Now().Add(reaperWriteTimeout))
	if _, err := reaper.pipe.WriteString(string([]byte{op, kind}) + payload + "\n"); err != nil {
		slog.Warn("process reaper unavailable; engine children can outlive an abrupt exit", "err", err)
		_ = reaper.pipe.Close()
		if reaper.cmd != nil {
			_ = reaper.cmd.Wait()
		}
		reaper.pipe = nil
		reaper.cmd = nil
		return false
	}
	return true
}

// reaperDrainTimeout bounds the wait for killed processes to exit before the
// directories they may still be writing are removed.
const reaperDrainTimeout = 5 * time.Second

// RunReaper is the companion's main loop. It records what the engine tracks and,
// once the engine is gone, kills each entry whose process or session member
// matches its registered incarnation, then removes each directory still tracked.
func RunReaper(in io.Reader) int {
	// Only the engine's exit ends reaping, so stray signals are ignored.
	signal.Ignore(syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	reap(in)
	return 0
}

// reap blocks until in closes, then kills what is still tracked and removes
// tracked directories once the killed processes are gone.
func reap(in io.Reader) {
	reapWithOps(in, defaultReaperOps())
}

func reapWithOps(in io.Reader, ops reaperOps) {
	entries := readReaperEntriesWithOps(in, ops)
	var killed []osprocess.Incarnation
	var dirs []string
	for _, entry := range entries {
		switch entry.kind {
		case reaperDir:
			dirs = append(dirs, entry.path)
		case reaperSupervisor:
			if ops.incarnationAlive(entry.leader) && signalSupervisor(entry.leader.PID, entry.leader.Start, true) {
				killed = appendIncarnation(killed, entry.leader)
			}
		case reaperProcess:
			if ops.signalProcess(entry.leader, syscall.SIGKILL) == nil {
				killed = appendIncarnation(killed, entry.leader)
			}
		case reaperGroup:
			killed = reapGroupEntry(entry, ops, killed)
		}
	}
	if len(dirs) == 0 {
		return
	}
	awaitSignalledGone(killed, ops.incarnationAlive, reaperDrainTimeout)
	for _, dir := range dirs {
		_ = os.RemoveAll(dir)
	}
}

func reapGroupEntry(entry *reaperEntry, ops reaperOps, killed []osprocess.Incarnation) []osprocess.Incarnation {
	targets := append([]osprocess.Incarnation(nil), entry.members...)
	if witness, ok := aliveGroupWitness(entry, ops.incarnationAlive); ok {
		for _, inc := range scanReaperSession(entry.leader.PID, witness, ops) {
			targets = appendIncarnation(targets, inc)
		}
	}
	if ops.incarnationAlive(entry.leader) {
		if ops.signalGroup(entry.leader, syscall.SIGKILL) == nil {
			killed = appendIncarnation(killed, entry.leader)
		}
		if ops.signalProcess(entry.leader, syscall.SIGKILL) == nil {
			killed = appendIncarnation(killed, entry.leader)
		}
	}
	for _, member := range targets {
		if ops.signalProcess(member, syscall.SIGKILL) == nil {
			killed = appendIncarnation(killed, member)
		}
	}
	return killed
}

// awaitSignalledGone waits, within one shared bound, until no killed process
// incarnation remains alive.
func awaitSignalledGone(targets []osprocess.Incarnation, alive func(osprocess.Incarnation) bool, within time.Duration) {
	deadline := time.Now().Add(within)
	for _, target := range targets {
		for alive(target) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
}
