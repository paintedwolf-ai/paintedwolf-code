package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

const evaluationHelperMode = "LYCAON_EVALUATION_PROCESS_HELPER"

func TestEvaluationProcessHelper(t *testing.T) {
	switch os.Getenv(evaluationHelperMode) {
	case "memory":
		data := make([]byte, 1<<20)
		for n := range data {
			data[n] = byte(n)
		}
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		runtime.KeepAlive(data)
	case "parent":
		executable, err := os.Executable()
		testutil.FailErr(t, "locate process helper", err)
		children := make([]*exec.Cmd, 2)
		for n := range children {
			children[n] = startEvaluationMemoryWorker(t, executable)
		}
		fmt.Printf("workers %d %d\n", children[0].Process.Pid, children[1].Process.Pid)
		for _, child := range children {
			_ = child.Wait()
		}
	}
}

func startEvaluationMemoryWorker(t *testing.T, executable string) *exec.Cmd {
	t.Helper()
	child := exec.Command(executable, "-test.run=^TestEvaluationProcessHelper$")
	child.Env = append(os.Environ(), evaluationHelperMode+"=memory")
	stdout, err := child.StdoutPipe()
	testutil.FailErr(t, "open memory worker readiness pipe", err)
	stdin, err := child.StdinPipe()
	testutil.FailErr(t, "open memory worker lifetime pipe", err)
	t.Cleanup(func() {
		_ = stdin.Close()
		if child.Process != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	testutil.FailErr(t, "start memory worker", child.Start())
	line, err := bufio.NewReader(stdout).ReadString('\n')
	testutil.FailErr(t, "await resident memory worker", err)
	if line != "ready\n" {
		t.Fatalf("unexpected worker readiness: %q", line)
	}
	return child
}

func TestEvaluationMeasuresWorkersAndCancelsOwnedGroup(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("process measurement platform")
	}
	executable, err := os.Executable()
	testutil.FailErr(t, "locate process helper", err)
	ctx, cancel := context.WithCancel(testutil.BoundedContext(t, 30*time.Second))
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestEvaluationProcessHelper$")
	cmd.Env = append(os.Environ(), evaluationHelperMode+"=parent")
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "open parent readiness pipe", err)
	process, err := newEvaluationProcess(cmd)
	testutil.FailErr(t, "create evaluation process", err)
	defer process.close()
	testutil.FailErr(t, "start evaluation process", cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			process.kill()
			_ = cmd.Wait()
		}
	}()
	testutil.FailErr(t, "track evaluation process", process.started(cmd.Process.Pid))
	line, err := bufio.NewReader(stdout).ReadString('\n')
	testutil.FailErr(t, "await allocated worker memory", err)
	var workers [2]int
	_, err = fmt.Sscanf(line, "workers %d %d", &workers[0], &workers[1])
	testutil.FailErr(t, "read started workers", err)
	memory, err := process.sample()
	testutil.FailErr(t, "measure resident workers", err)
	// Allocated bytes need not remain resident under host memory pressure.
	if memory.RSSBytes < 0 || memory.Processes != 3 {
		t.Fatalf("parent and both workers not measured: %+v", memory)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancellation did not terminate the scanner process")
	}
	for _, pid := range workers {
		testutil.WaitFor(t, 30*time.Second, func() bool {
			alive, err := evaluationWorkerAlive(pid)
			testutil.FailErr(t, "check owned worker", err)
			return !alive
		})
	}
}
