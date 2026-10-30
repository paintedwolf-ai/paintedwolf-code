//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type lineageBirth struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
}

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
		child, birth, err := startLineageChild("--lineage-leaf", true)
		if err != nil {
			os.Exit(2)
		}
		_ = json.NewEncoder(os.Stdout).Encode(birth)
		_ = child.Process.Release()
		return true
	case "--lineage-racer":
		for index := 0; index < 32; index++ {
			child, birth, err := startLineageChild("--lineage-leaf", true)
			if err != nil {
				os.Exit(2)
			}
			if index == 0 {
				_ = json.NewEncoder(os.Stdout).Encode(birth)
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

func startLineageChild(role string, session bool) (*exec.Cmd, lineageBirth, error) {
	child := exec.Command(lineageImage(), role)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: session}
	if err := child.Start(); err != nil {
		return nil, lineageBirth{}, err
	}
	birth, err := lineageProcessBirth(child.Process.Pid, os.Getpid())
	if err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		return nil, lineageBirth{}, err
	}
	return child, birth, nil
}

// A creator records starttime while its exact child still has this parent.
// A replacement PID is not accepted merely because it has the caller's UID.
func lineageProcessBirth(pid, parent int) (lineageBirth, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return lineageBirth{}, err
	}
	return decodeLineageBirth(raw, pid, parent)
}
func decodeLineageBirth(raw []byte, pid, parent int) (lineageBirth, error) {
	text := string(raw)
	end := strings.LastIndexByte(text, ')')
	begin := strings.IndexByte(text, '(')
	if begin < 1 || end < begin {
		return lineageBirth{}, fmt.Errorf("process birth lacks command field")
	}
	actual, e1 := strconv.Atoi(strings.TrimSpace(text[:begin]))
	fields := strings.Fields(text[end+1:])
	if e1 != nil || actual != pid || len(fields) < 20 {
		return lineageBirth{}, fmt.Errorf("process birth PID differs")
	}
	owner, e2 := strconv.Atoi(fields[1])
	start, e3 := strconv.ParseUint(fields[19], 10, 64)
	if e2 != nil || e3 != nil || start == 0 || (parent > 0 && owner != parent) {
		return lineageBirth{}, fmt.Errorf("process birth parent differs")
	}
	return lineageBirth{pid, start}, nil
}

func lineageProbe() ([]lineageBirth, error) {
	direct, birth, err := startLineageChild("--lineage-leaf", true)
	if err != nil {
		return nil, err
	}
	births := []lineageBirth{birth}
	_ = direct.Process.Release()
	orphan := exec.Command(lineageImage(), "--lineage-orphan-parent")
	raw, err := orphan.Output()
	if err != nil {
		return nil, err
	}
	var orphanBirth lineageBirth
	if err := json.Unmarshal(raw, &orphanBirth); err != nil || orphanBirth.PID <= 1 || orphanBirth.Start == 0 {
		return nil, fmt.Errorf("orphan did not report its engine birth")
	}
	births = append(births, orphanBirth)
	racer := exec.Command(lineageImage(), "--lineage-racer")
	output, err := racer.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := racer.Start(); err != nil {
		return nil, err
	}
	racerBirth, err := lineageProcessBirth(racer.Process.Pid, os.Getpid())
	if err != nil {
		_ = racer.Process.Kill()
		_ = racer.Wait()
		return nil, err
	}
	line, err := bufio.NewReader(output).ReadString('\n')
	_ = output.Close()
	var first lineageBirth
	parseErr := json.Unmarshal([]byte(line), &first)
	if err != nil || parseErr != nil || first.PID <= 1 || first.Start == 0 {
		_ = racer.Process.Kill()
		_ = racer.Wait()
		return nil, fmt.Errorf("racing engine did not report first descendant birth")
	}
	births = append(births, racerBirth, first)
	_ = racer.Process.Release()
	return births, nil
}
