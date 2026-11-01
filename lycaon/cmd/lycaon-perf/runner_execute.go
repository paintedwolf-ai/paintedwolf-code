package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *runState) runWorkload(ctx context.Context) error {
	return s.exec().executeWorkload(ctx)
}

func (s runExecutor) executeWorkload(ctx context.Context) error {
	if err := s.createProjectAndSession(ctx); err != nil {
		return err
	}
	eventCtx, stopEvents := context.WithCancel(ctx)
	monitor, err := s.client.startEventMonitor(eventCtx, s.project.ID, "")
	if err != nil {
		return err
	}
	if s.cfg.installed {
		return s.runInstalledWorkload(ctx, monitor, stopEvents)
	}
	if err := s.scenarios().runPromptScenario(ctx, -1, false); err != nil {
		stopEvents()
		<-monitor.done
		return fmt.Errorf("warm prompt: %w", err)
	}
	s.resources.markBaseline(ctx)
	s.workloadBaseline = time.Now()
	if err := s.scenarios().runQueryScenarios(ctx); err != nil {
		stopEvents()
		<-monitor.done
		return err
	}
	if err := s.scenarios().runPromptScenarios(ctx, s.cfg.prompts); err != nil {
		stopEvents()
		<-monitor.done
		return err
	}
	stopEvents()
	<-monitor.done
	cursor, monitorErr := s.addEventMonitor(monitor)
	if monitorErr != nil {
		return monitorErr
	}
	if s.cfg.soak > 0 {
		if strings.TrimSpace(cursor) == "" {
			return fmt.Errorf("event stream produced no restart cursor")
		}
		if err := s.restartAndVerify(ctx); err != nil {
			return err
		}
		eventCtx, stopEvents = context.WithCancel(ctx)
		oldMonitor, replayErr := s.client.startEventMonitor(eventCtx, s.project.ID, cursor)
		if replayErr == nil {
			stopEvents()
			<-oldMonitor.done
			return fmt.Errorf("old event cursor after restart unexpectedly remained available")
		}
		if !errors.Is(replayErr, errEventReplayUnavailable) {
			stopEvents()
			return fmt.Errorf("old event cursor after restart, want replay unavailable: %w", replayErr)
		}
		s.correctness.ReplayResetRecovered = true
		monitor, err = s.client.startEventMonitor(eventCtx, s.project.ID, "")
		if err != nil {
			stopEvents()
			return err
		}
		if err := s.scenarios().runPromptScenario(ctx, -2, false); err != nil {
			stopEvents()
			<-monitor.done
			return fmt.Errorf("restart warm prompt: %w", err)
		}
		s.resources.markBaseline(ctx)
		s.workloadBaseline = time.Now()
		if err := s.scenarios().runSoak(ctx, s.cfg.soak); err != nil {
			stopEvents()
			<-monitor.done
			return err
		}
		stopEvents()
		<-monitor.done
		s.correctness.PostRestartEvents = monitor.countSnapshot()
		_, monitorErr = s.addEventMonitor(monitor)
		return monitorErr
	}
	return nil
}

// runInstalledWorkload measures the release-valid scenarios: without a mock provider an
// installed engine takes no prompt turns.
func (s runExecutor) runInstalledWorkload(ctx context.Context, monitor *eventMonitor, stopEvents context.CancelFunc) error {
	s.resources.markBaseline(ctx)
	s.workloadBaseline = time.Now()
	err := s.scenarios().runQueryScenarios(ctx)
	stopEvents()
	<-monitor.done
	if err != nil {
		return err
	}
	_, err = s.addEventMonitor(monitor)
	return err
}

func (s runExecutor) addEventMonitor(monitor *eventMonitor) (string, error) {
	count, duplicates, cursor, err := monitor.snapshot()
	s.correctness.EventCount += count
	s.correctness.DuplicateEventIDs += duplicates
	return cursor, err
}

func (s runExecutor) restartAndVerify(ctx context.Context) error {
	if err := s.stopSidecar(); err != nil {
		return fmt.Errorf("stop before restart: %w", err)
	}
	if err := s.startSidecar(ctx); err != nil {
		return fmt.Errorf("restart sidecar: %w", err)
	}
	s.correctness.Restarts++
	bootstrap, err := s.client.bootstrap(ctx, s.session.ID)
	if err != nil {
		return fmt.Errorf("bootstrap after restart: %w", err)
	}
	if bootstrap.Session.ID != s.session.ID || len(bootstrap.Transcript.Messages) == 0 {
		return fmt.Errorf("restart did not recover session %s", s.session.ID)
	}
	s.correctness.RestartRecovered = true
	return nil
}

func (s runExecutor) createProjectAndSession(ctx context.Context) error {
	started := time.Now()
	err := s.client.request(ctx, http.MethodPost, "/v1/projects", api.CreateProjectRequest{
		Roots: []api.CreateProjectRootInput{{Path: s.projectDir}},
	}, &s.project)
	s.metrics.add("project.create", elapsedMS(started))
	if err != nil {
		return err
	}
	started = time.Now()
	err = s.client.request(ctx, http.MethodPost, "/v1/sessions", api.CreateSessionRequest{
		ProjectID: s.project.ID, Posture: api.SessionPostureBuild,
	}, &s.session)
	s.metrics.add("session.accept", elapsedMS(started))
	if err != nil {
		return err
	}
	started = time.Now()
	if err := s.client.waitPrepared(ctx, s.session.ID); err != nil {
		return err
	}
	s.metrics.add("session.prepare", elapsedMS(started))
	started = time.Now()
	if _, err := s.client.bootstrap(ctx, s.session.ID); err != nil {
		return err
	}
	s.metrics.add("session.bootstrap.cold", elapsedMS(started))
	return nil
}

func (s runScenarioRunner) runQueryScenarios(ctx context.Context) error {
	for range s.cfg.iterations {
		started := time.Now()
		if _, err := s.client.bootstrap(ctx, s.session.ID); err != nil {
			return err
		}
		s.metrics.add("session.bootstrap.warm", elapsedMS(started))

		if err := s.measureSourceTree(ctx); err != nil {
			return err
		}
	}
	return s.runConcurrentBootstrap(ctx)
}

func (s runScenarioRunner) runConcurrentBootstrap(ctx context.Context) error {
	const concurrency = 8
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range max(1, s.cfg.iterations/4) {
				started := time.Now()
				if _, err := s.client.bootstrap(ctx, s.session.ID); err != nil {
					errs <- err
					return
				}
				s.metrics.add("session.bootstrap.concurrent", elapsedMS(started))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (s runScenarioRunner) runPromptScenarios(ctx context.Context, count int) error {
	for index := range count {
		if err := s.runPromptScenario(ctx, index, true); err != nil {
			return err
		}
	}
	return nil
}

func (s runScenarioRunner) runPromptScenario(ctx context.Context, index int, measured bool) error {
	bootstrap, err := s.client.bootstrap(ctx, s.session.ID)
	if err != nil {
		return err
	}
	baseline := messageIDs(bootstrap.Transcript.Messages)
	started := time.Now()
	var accepted api.PromptAcceptedResponse
	err = s.client.request(ctx, http.MethodPost, "/v1/sessions/"+s.session.ID+"/prompts", api.PromptRequest{
		OperationID: uuid.NewString(), Text: fmt.Sprintf("performance turn %d", index),
	}, &accepted)
	if measured {
		s.metrics.add("prompt.accept", elapsedMS(started))
	}
	if err != nil {
		return err
	}
	started = time.Now()
	settled, err := s.client.waitPromptSettled(ctx, s.session.ID, baseline)
	if measured {
		s.metrics.add("prompt.settle", elapsedMS(started))
	}
	if err != nil {
		return err
	}
	messageID := newAssistantMessageID(settled.Transcript.Messages, baseline)
	if messageID == "" {
		return fmt.Errorf("prompt turn %d settled without an assistant message", index)
	}
	started = time.Now()
	if err := s.client.replayStream(ctx, s.session.ID, messageID); err != nil {
		return err
	}
	if measured {
		s.metrics.add("prompt.stream_replay", elapsedMS(started))
		s.correctness.PromptTurnsSettled++
		s.correctness.StreamReplaysDone++
	}
	return nil
}
