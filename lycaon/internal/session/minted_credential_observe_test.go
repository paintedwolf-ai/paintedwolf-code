package session

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

type countingMintSource struct{ calls atomic.Int32 }

func (s *countingMintSource) MintedCredentialRule(hitl.ProposedAction) (hitl.DetectionMatch, bool) {
	s.calls.Add(1)
	return hitl.DetectionMatch{}, false
}

func TestMintedCredentialObservationTracksReloads(t *testing.T) {
	t.Parallel()
	manager := &Manager{}
	var published atomic.Pointer[countingMintSource]
	manager.SetMintedCredentialSource(func() MintedCredentialSource {
		if source := published.Load(); source != nil {
			return source
		}
		return nil
	})
	manager.SetRememberSecrets(func(string, []secretmatch.Remembered) { t.Error("unmatched output was remembered") })
	session := &api.Session{ID: "session"}
	observe := func() { manager.ObserveMintedCredential(t.Context(), session, "command", nil, "output") }
	first, second := &countingMintSource{}, &countingMintSource{}
	published.Store(first)
	observe()
	published.Store(nil)
	observe()
	published.Store(second)
	observe()
	if first.calls.Load() != 1 || second.calls.Load() != 1 {
		t.Fatalf("detector calls = %d, %d; want one per installed source", first.calls.Load(), second.calls.Load())
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 100 {
			published.Store(nil)
			published.Store(first)
			published.Store(second)
		}
	})
	for range 3 {
		workers.Go(func() {
			for range 100 {
				observe()
			}
		})
	}
	workers.Wait()
	published.Store(nil)
	calls := first.calls.Load() + second.calls.Load()
	observe()
	if first.calls.Load()+second.calls.Load() != calls {
		t.Fatal("removed detector received an observation")
	}
}
