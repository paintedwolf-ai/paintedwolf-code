package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

func evaluationWorkerAlive(pid int) (bool, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	// A process reaped while its stat file is open reads as ESRCH.
	if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fields := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z", nil
}
