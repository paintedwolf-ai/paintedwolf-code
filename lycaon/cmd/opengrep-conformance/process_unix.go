//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
)

type evaluationProcess struct {
	pid atomic.Int64
	cmd *exec.Cmd
}

func newEvaluationProcess(cmd *exec.Cmd) (*evaluationProcess, error) {
	process := &evaluationProcess{cmd: cmd}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { process.kill(); return nil }
	return process, nil
}

func (p *evaluationProcess) started(pid int) error { p.pid.Store(int64(pid)); return nil }
func (p *evaluationProcess) close()                {}
func (p *evaluationProcess) kill() {
	pid := p.pid.Load()
	if pid == 0 && p.cmd.Process != nil {
		pid = int64(p.cmd.Process.Pid)
	}
	if pid > 0 {
		_ = syscall.Kill(-int(pid), syscall.SIGKILL)
	}
}
func (p *evaluationProcess) sample() (processMemorySample, error) {
	return processGroupMemory(int(p.pid.Load()))
}

func vanishedProcess(err error) bool { return os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) }
