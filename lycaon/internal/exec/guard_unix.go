//go:build unix

package exec

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// DefaultBelowNormalNice is the nice value applied for ProcessPriorityBelowNormal.
const DefaultBelowNormalNice = 5

// supervisorLaunchGrace bounds how long a held termination waits for the
// supervisor's launch report. It covers a slow re-exec on a loaded host, and it
// keeps a supervisor that never reports from blocking a caller without a WaitDelay.
const supervisorLaunchGrace = 10 * time.Second

type guardOps struct {
	sessionMembers   func(int) []int
	incarnationOf    func(int) (osprocess.Incarnation, bool)
	incarnationAlive func(osprocess.Incarnation) bool
	signalProcess    func(osprocess.Incarnation, syscall.Signal) error
	signalGroup      func(osprocess.Incarnation, syscall.Signal) error
	awaitExit        func(int)
}

func defaultGuardOps() guardOps {
	return guardOps{
		sessionMembers:   osprocess.SessionMembers,
		incarnationOf:    osprocess.IncarnationOf,
		incarnationAlive: osprocess.Incarnation.Alive,
		signalProcess:    osprocess.Signal,
		signalGroup:      osprocess.SignalGroup,
		awaitExit:        awaitLeaderExit,
	}
}

type guardTimers struct {
	launchGrace time.Duration
	launch      *time.Timer
	kill        *time.Timer
	released    bool
}

type unixGuard struct {
	belowNormal bool
	supervised  bool
	leader      osprocess.Incarnation
	// untrack releases the group from the reaper that kills it if the engine dies.
	untrack func()
	ops     guardOps

	mu sync.Mutex
	// reaped is set under mu after surviving session members are snapshotted and
	// before cmd.Wait collects the leader.
	reaped   bool
	snapshot []osprocess.Incarnation
	// terminated marks a tree that was asked to exit; release then sweeps
	// whatever outlived the leader.
	terminated bool
	// awaitingLaunch holds termination until a supervised command reports its
	// launch: before the supervisor installs its handlers a signal kills it,
	// and the parent would see a failed start instead of a terminated command.
	awaitingLaunch bool
	// heldTerminate is a termination requested while awaitingLaunch.
	heldTerminate bool
	timers        guardTimers
}

func newRunGuard(priority ProcessPriority) (runGuard, error) {
	return &unixGuard{
		belowNormal: priority == ProcessPriorityBelowNormal,
		ops:         defaultGuardOps(),
		timers:      guardTimers{launchGrace: supervisorLaunchGrace},
	}, nil
}

func (g *unixGuard) configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// A new session is its own group with no controlling terminal; a bare group
	// under a terminal is a background job that stops on its first terminal read.
	// A session leader cannot also Setpgid.
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setpgid = false
	g.awaitingLaunch = commandSupervisor(cmd)
	// Cancellation terminates the entire session the leader started.
	cmd.Cancel = func() error {
		g.terminate(cmd)
		return nil
	}
}

func (g *unixGuard) onStarted(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	g.mu.Lock()
	g.supervised = commandSupervisor(cmd)
	g.recordProcessGroupLocked(cmd.Process.Pid)
	g.mu.Unlock()
	if err := g.confirmLaunch(cmd); err != nil {
		return err
	}
	if !g.belowNormal {
		return nil
	}
	// Below normal never raises priority: a child that inherited a higher nice
	// keeps it, and lowering a nice value needs privilege.
	nice, err := processNice(cmd.Process.Pid)
	if err == nil && nice < DefaultBelowNormalNice {
		err = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, DefaultBelowNormalNice)
	}
	if errors.Is(err, syscall.ESRCH) {
		// Fast children may exit before priority assignment.
		return nil
	}
	return err
}

// recordProcessGroupLocked stores the session leader's incarnation.
func (g *unixGuard) recordProcessGroupLocked(pid int) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid != pid {
		return
	}
	inc, ok := g.ops.incarnationOf(pid)
	if !ok {
		return
	}
	g.leader = inc
	if g.supervised {
		g.untrack = trackIncarnation(reaperSupervisor, inc)
	} else {
		g.untrack = trackIncarnation(reaperGroup, inc)
	}
}

// confirmLaunch reads the supervisor's launch report, then delivers a
// termination requested while the launch was unconfirmed.
func (g *unixGuard) confirmLaunch(cmd *exec.Cmd) error {
	err := supervisorStartupError(cmd)
	g.mu.Lock()
	held := g.heldTerminate && err == nil
	g.awaitingLaunch, g.heldTerminate = false, false
	if g.timers.launch != nil {
		g.timers.launch.Stop()
		g.timers.launch = nil
	}
	g.mu.Unlock()
	if held {
		g.deliverTerminate(cmd)
	}
	return err
}

func (g *unixGuard) wait(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	if cmd.Process != nil {
		g.ops.awaitExit(cmd.Process.Pid)
	}
	g.mu.Lock()
	g.snapshotBeforeReapLocked()
	g.mu.Unlock()
	return cmd.Wait()
}

func (g *unixGuard) snapshotBeforeReapLocked() {
	if g.reaped {
		return
	}
	if g.leader.PID > 0 && !g.supervised {
		for _, pid := range g.ops.sessionMembers(g.leader.PID) {
			if pid <= 0 || pid == g.leader.PID {
				continue
			}
			if inc, ok := g.ops.incarnationOf(pid); ok {
				g.snapshot = appendIncarnation(g.snapshot, inc)
			}
		}
		if len(g.snapshot) > 0 {
			trackReaperMembers(g.leader.PID, g.snapshot)
		}
	}
	g.reaped = true
}

// terminate sends SIGTERM to the tree and arms a SIGKILL for whatever is still
// running after TerminateGrace. A second call is a no-op.
func (g *unixGuard) terminate(cmd *exec.Cmd) {
	g.mu.Lock()
	if g.terminated {
		g.mu.Unlock()
		return
	}
	g.terminated = true
	if g.awaitingLaunch {
		g.heldTerminate = true
		g.timers.launch = time.AfterFunc(g.timers.launchGrace, func() { g.abandonLaunch(cmd) })
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	g.deliverTerminate(cmd)
}

// abandonLaunch SIGKILLs a supervisor that has not reported its launch; the
// supervisor ignores SIGUSR2 until it installs handlers, so the hard path through
// signalSupervisor cannot stop it. Its PID and session stay reserved until the
// caller's Wait, and the startup report then reads EOF.
func (g *unixGuard) abandonLaunch(cmd *exec.Cmd) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.awaitingLaunch || cmd == nil || cmd.Process == nil {
		return
	}
	g.timers.launch = nil
	leader := g.leader
	if leader.PID <= 0 {
		leader, _ = g.ops.incarnationOf(cmd.Process.Pid)
	}
	if leader.PID > 0 {
		_ = g.ops.signalGroup(leader, syscall.SIGKILL)
		_ = g.ops.signalProcess(leader, syscall.SIGKILL)
	}
}

func (g *unixGuard) deliverTerminate(cmd *exec.Cmd) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.signalLocked(cmd, syscall.SIGTERM) || g.timers.released {
		return
	}
	if g.timers.kill != nil {
		g.timers.kill.Stop()
	}
	g.timers.kill = time.AfterFunc(TerminateGrace, func() { g.kill(cmd) })
}

func (g *unixGuard) kill(cmd *exec.Cmd) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.terminated = true
	if g.timers.kill != nil {
		g.timers.kill.Stop()
		g.timers.kill = nil
	}
	g.signalLocked(cmd, syscall.SIGKILL)
}

// signalLocked delivers sig while holding g.mu. Before the leader is reaped,
// the group and session are reserved and signalled through verified
// incarnations. After the leader is reaped, only snapshotted member
// incarnations (plus any new session members discovered while a snapshot member
// stays alive across the scan) are signalled.
func (g *unixGuard) signalLocked(cmd *exec.Cmd, sig syscall.Signal) bool {
	if g.supervised && g.leader.PID > 0 {
		return signalSupervisor(g.leader.PID, g.leader.Start, sig == syscall.SIGKILL)
	}
	if g.leader.PID <= 0 {
		if g.reaped || cmd == nil || cmd.Process == nil {
			return false
		}
		inc, ok := g.ops.incarnationOf(cmd.Process.Pid)
		if !ok {
			return false
		}
		_ = g.ops.signalProcess(inc, sig)
		return true
	}
	if !g.reaped {
		for _, inc := range g.scanSessionLocked(g.leader) {
			g.snapshot = appendIncarnation(g.snapshot, inc)
		}
		_ = g.ops.signalGroup(g.leader, sig)
		_ = g.ops.signalProcess(g.leader, sig)
		for _, inc := range g.snapshot {
			_ = g.ops.signalProcess(inc, sig)
		}
		return true
	}
	if witness, ok := g.aliveSnapshotMemberLocked(); ok {
		discovered := g.scanSessionLocked(witness)
		if len(discovered) > 0 {
			trackReaperMembers(g.leader.PID, discovered)
			for _, inc := range discovered {
				g.snapshot = appendIncarnation(g.snapshot, inc)
			}
		}
	}
	for _, inc := range g.snapshot {
		_ = g.ops.signalProcess(inc, sig)
	}
	return len(g.snapshot) > 0
}

func (g *unixGuard) scanSessionLocked(witness osprocess.Incarnation) []osprocess.Incarnation {
	if !g.ops.incarnationAlive(witness) {
		return nil
	}
	pids := g.ops.sessionMembers(g.leader.PID)
	var members []osprocess.Incarnation
	for _, pid := range pids {
		if pid <= 0 || pid == g.leader.PID {
			continue
		}
		if inc, ok := g.ops.incarnationOf(pid); ok {
			members = appendIncarnation(members, inc)
		}
	}
	if !g.ops.incarnationAlive(witness) {
		return nil
	}
	return members
}

func (g *unixGuard) aliveSnapshotMemberLocked() (osprocess.Incarnation, bool) {
	for _, inc := range g.snapshot {
		if g.ops.incarnationAlive(inc) {
			return inc, true
		}
	}
	return osprocess.Incarnation{}, false
}

// release runs after the leader is reaped. A terminated tree gets one final
// sweep so nothing that outlived the leader survives the command.
func (g *unixGuard) release() {
	g.mu.Lock()
	g.timers.released = true
	if g.timers.launch != nil {
		g.timers.launch.Stop()
		g.timers.launch = nil
	}
	if g.timers.kill != nil {
		g.timers.kill.Stop()
		g.timers.kill = nil
	}
	if g.terminated {
		g.signalLocked(nil, syscall.SIGKILL)
	}
	untrack := g.untrack
	if !g.terminated {
		if _, alive := g.aliveSnapshotMemberLocked(); alive {
			untrack = nil
		}
	}
	g.mu.Unlock()
	if untrack != nil {
		untrack()
	}
}

func appendIncarnation(list []osprocess.Incarnation, candidate osprocess.Incarnation) []osprocess.Incarnation {
	for _, existing := range list {
		if existing == candidate {
			return list
		}
	}
	return append(list, candidate)
}
