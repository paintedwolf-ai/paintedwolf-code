//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// These fixed roles execute the generation's engine image. Detached sessions
// and orphaning must not move a protected descendant into the ordinary broker.
func lineageRole() bool {
	if len(os.Args) != 2 {
		return false
	}
	switch os.Args[1] {
	case "--lineage-leaf":
		for {
			time.Sleep(time.Hour)
		}
	case "--lineage-orphan-parent":
		child, err := startLineageChild("--lineage-leaf", true)
		if err != nil {
			os.Exit(2)
		}
		fmt.Println(child.Process.Pid)
		_ = child.Process.Release()
		return true
	case "--lineage-racer":
		for index := 0; index < 32; index++ {
			child, err := startLineageChild("--lineage-leaf", true)
			if err != nil {
				os.Exit(2)
			}
			if index == 0 {
				fmt.Println(child.Process.Pid)
			}
			_ = child.Process.Release()
			time.Sleep(5 * time.Millisecond)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	return false
}

func lineageImage() string { return filepath.Join(filepath.Dir(os.Args[0]), "pw") }

func startLineageChild(role string, session bool) (*exec.Cmd, error) {
	child := exec.Command(lineageImage(), role)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: session}
	return child, child.Start()
}

func lineageProbe() ([]int, error) {
	direct, err := startLineageChild("--lineage-leaf", true)
	if err != nil {
		return nil, err
	}
	pids := []int{direct.Process.Pid}
	_ = direct.Process.Release()
	orphan := exec.Command(lineageImage(), "--lineage-orphan-parent")
	raw, err := orphan.Output()
	if err != nil {
		return nil, err
	}
	orphanPID, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || orphanPID <= 1 {
		return nil, fmt.Errorf("orphan did not report its engine descendant")
	}
	pids = append(pids, orphanPID)
	racer := exec.Command(lineageImage(), "--lineage-racer")
	output, err := racer.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := racer.Start(); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(output).ReadString('\n')
	_ = output.Close()
	first, parseErr := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || parseErr != nil || first <= 1 {
		return nil, fmt.Errorf("racing engine did not report first descendant")
	}
	pids = append(pids, racer.Process.Pid, first)
	_ = racer.Process.Release()
	return pids, nil
}
