package session

import (
	"context"
	"sync"
	"testing"
)

func TestPromptCurationConcurrentAdmissionAndDrain(t *testing.T) {
	m := &promptCurations{}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 250 {
				work := m.Register("root", func() {})
				go m.Finish("root", work)
				m.Wait(t.Context())
			}
		})
	}
	workers.Wait()
	m.Wait(t.Context())
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bySession) != 0 {
		t.Fatal("completed curation remains registered")
	}
}

func TestPromptCurationDrainIncludesEveryRegisteredSession(t *testing.T) {
	m := &promptCurations{}
	first := m.Register("first", func() {})
	second := m.Register("second", func() {})
	m.Finish("first", first)
	select {
	case <-m.idle:
		t.Fatal("drain completed while another session still had work")
	default:
	}
	m.Finish("second", second)
	m.Wait(t.Context())
	select {
	case <-first.done:
	default:
		t.Fatal("first session completion was lost")
	}
	select {
	case <-second.done:
	default:
		t.Fatal("second session completion was lost")
	}
}

func TestPromptCurationDrainHonorsShutdownCancellation(t *testing.T) {
	m := &Manager{}
	work := m.curation.Register("root", func() {})
	defer m.curation.Finish("root", work)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m.WaitForPromptCuration(ctx)
	select {
	case <-work.done:
		t.Fatal("canceling the drain completed unfinished curation")
	default:
	}
}
