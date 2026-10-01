//go:build !darwin && !linux && !windows

package main

func evaluationWorkerAlive(_ int) (bool, error) { return false, nil }
