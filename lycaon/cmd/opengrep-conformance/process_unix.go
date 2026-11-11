//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/lycaon/lycaon/internal/osprocess"
)

type evaluationProcess struct {
	mu      sync.Mutex
	leader  osprocess.Incarnation
	members []osprocess.Incarnation
	cmd     *exec.Cmd
}

func newEvaluationProcess(cmd *exec.Cmd) (*evaluationProcess, error) {
	process := &evaluationProcess{cmd: cmd}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { process.kill(); return nil }
	return process, nil
}

func (p *evaluationProcess) started(pid int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if inc, ok := osprocess.IncarnationOf(pid); ok {
		p.leader = inc
	} else {
		p.leader = osprocess.Incarnation{PID: pid}
	}
	return nil
}

func (p *evaluationProcess) close() {}

func (p *evaluationProcess) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.leader.PID <= 0 && p.cmd.Process != nil {
		if inc, ok := osprocess.IncarnationOf(p.cmd.Process.Pid); ok {
			p.leader = inc
		}
	}
	if p.leader.PID <= 0 {
		return
	}
	if p.leader.Alive() {
		p.snapshotSessionLocked()
		_ = osprocess.SignalGroup(p.leader, syscall.SIGKILL)
		_ = osprocess.Signal(p.leader, syscall.SIGKILL)
	}
	for _, member := range p.members {
		_ = osprocess.Signal(member, syscall.SIGKILL)
	}
}

func (p *evaluationProcess) sample() (processMemorySample, error) {
	p.mu.Lock()
	pid := p.leader.PID
	if p.leader.Alive() {
		p.snapshotSessionLocked()
	}
	p.mu.Unlock()
	return processGroupMemory(pid)
}

func (p *evaluationProcess) snapshotSessionLocked() {
	for _, pid := range osprocess.SessionMembers(p.leader.PID) {
		if pid <= 0 || pid == p.leader.PID {
			continue
		}
		inc, ok := osprocess.IncarnationOf(pid)
		if !ok {
			continue
		}
		seen := false
		for _, existing := range p.members {
			if existing == inc {
				seen = true
				break
			}
		}
		if !seen {
			p.members = append(p.members, inc)
		}
	}
}

func vanishedProcess(err error) bool { return os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) }
