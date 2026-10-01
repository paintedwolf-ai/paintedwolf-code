package main

import (
	"os/exec"
	"sync"
	"time"
)

const resourceSampleInterval = 20 * time.Millisecond

type processMemorySample struct {
	RSSBytes  int64
	Processes int
}

type processResources struct {
	PeakRSSBytes int64
	Samples      int
	Failure      string
}

func runEvaluationProcess(cmd *exec.Cmd) (processResources, error) {
	process, err := newEvaluationProcess(cmd)
	if err != nil {
		return processResources{}, err
	}
	defer process.close()
	if err := cmd.Start(); err != nil {
		return processResources{}, err
	}
	if err := process.started(cmd.Process.Pid); err != nil {
		process.kill()
		_ = cmd.Wait()
		return processResources{}, err
	}
	var resources processResources
	var sampled sync.WaitGroup
	stop := make(chan struct{})
	sampled.Go(func() {
		ticker := time.NewTicker(resourceSampleInterval)
		defer ticker.Stop()
		for {
			memory, sampleErr := process.sample()
			if sampleErr != nil {
				resources.Failure = sampleErr.Error()
			}
			if memory.RSSBytes > 0 {
				resources.Samples++
				resources.PeakRSSBytes = max(resources.PeakRSSBytes, memory.RSSBytes)
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	})
	runErr := cmd.Wait()
	close(stop)
	sampled.Wait()
	process.kill()
	if resources.Samples == 0 && resources.Failure == "" {
		resources.Failure = "no process memory samples"
	}
	return resources, runErr
}
