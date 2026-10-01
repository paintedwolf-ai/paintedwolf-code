package main

import (
	"fmt"
	"os"
	"strings"
)

func evaluationWorkerAlive(pid int) (bool, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fields := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
	return len(fields) > 0 && fields[0] != "Z", nil
}
