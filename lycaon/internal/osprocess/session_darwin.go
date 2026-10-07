//go:build darwin

package osprocess

import "golang.org/x/sys/unix"

// SessionMembers lists every process whose session id is sid, whatever
// process group it moved itself into. The leader is included while it exists.
func SessionMembers(sid int) []int {
	if sid <= 0 {
		return nil
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	var members []int
	for i := range procs {
		pid := int(procs[i].Proc.P_pid)
		if pid <= 0 {
			continue
		}
		// Another user's process answers EPERM and is not ours to signal anyway.
		if got, err := unix.Getsid(pid); err == nil && got == sid {
			members = append(members, pid)
		}
	}
	return members
}
