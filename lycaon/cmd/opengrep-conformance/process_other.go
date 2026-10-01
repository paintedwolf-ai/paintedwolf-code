//go:build !darwin && !linux && !windows

package main

import (
	"fmt"
	"os/exec"
)

type evaluationProcess struct{}

func newEvaluationProcess(_ *exec.Cmd) (*evaluationProcess, error) {
	return nil, fmt.Errorf("unsupported conformance process platform")
}
func (*evaluationProcess) started(_ int) error { return nil }
func (*evaluationProcess) close()              {}
func (*evaluationProcess) kill()               {}
func (*evaluationProcess) sample() (processMemorySample, error) {
	return processMemorySample{}, fmt.Errorf("process memory measurement unavailable")
}
