//go:build darwin && cgo

package main

/*
#include <libproc.h>
#include <errno.h>
static int evaluation_resident_bytes(int pid, uint64_t *rss) {
 struct proc_taskinfo info;
 int size = proc_pidinfo(pid, PROC_PIDTASKINFO, 0, &info, sizeof(info));
 if (size != sizeof(info)) return errno ? errno : EIO;
 *rss = info.pti_resident_size;
 return 0;
}
*/
import "C"

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func processGroupMemory(group int) (processMemorySample, error) {
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", group)
	if err != nil {
		return processMemorySample{}, err
	}
	var memory processMemorySample
	for _, member := range members {
		var resident C.uint64_t
		code := C.evaluation_resident_bytes(C.int(member.Proc.P_pid), &resident)
		if code != 0 {
			if vanishedProcess(syscall.Errno(code)) {
				continue
			}
			return processMemorySample{}, fmt.Errorf("read process memory: %w", syscall.Errno(code))
		}
		memory.RSSBytes += int64(resident)
		memory.Processes++
	}
	return memory, nil
}
