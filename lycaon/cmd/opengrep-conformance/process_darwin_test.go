package main

import "golang.org/x/sys/unix"

func evaluationWorkerAlive(pid int) (bool, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	return len(processes) != 0 && processes[0].Proc.P_stat != 5, nil
}
