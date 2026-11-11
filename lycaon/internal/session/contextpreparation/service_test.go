package contextpreparation

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFocusCoalescesAndChangedCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int32
		var cancels atomic.Int32
		s := New(func(ctx context.Context, id string) (Candidate, error) {
			return Candidate{Key: id, Run: func(ctx context.Context) bool {
				runs.Add(1)
				<-ctx.Done()
				cancels.Add(1)
				return false
			}}, nil
		})
		s.Focus(t.Context(), "a", "p")
		synctest.Wait()
		for range 100 {
			s.Focus(t.Context(), "a", "p")
		}
		s.Changed("other", "p")
		synctest.Wait()
		if runs.Load() != 1 || cancels.Load() != 0 {
			t.Fatal("unchanged focus interrupted preparation")
		}
		s.Focus(t.Context(), "b", "p")
		synctest.Wait()
		if runs.Load() != 2 || cancels.Load() != 1 {
			t.Fatal("focus switch did not replace active preparation")
		}
		s.Stop()
		if err := s.Wait(t.Context()); err != nil {
			t.Fatalf("join preparation: %v", err)
		}
		if cancels.Load() != 2 {
			t.Fatal("shutdown did not join cancellation")
		}
		s.Focus(t.Context(), "c", "p")
		synctest.Wait()
		if runs.Load() != 2 {
			t.Fatal("work started after shutdown")
		}
	})
}

func TestCompletedCandidateAndRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int32
		s := New(func(context.Context, string) (Candidate, error) {
			return Candidate{Key: "prefix", Run: func(context.Context) bool { return runs.Add(1) > 1 }}, nil
		})
		defer s.Stop()
		s.Focus(t.Context(), "a", "p")
		synctest.Wait()
		s.Retry("a")
		synctest.Wait()
		s.Retry("a")
		synctest.Wait()
		if runs.Load() != 2 {
			t.Fatal("miss was not retried or completed work was repeated")
		}
		s.Observe(api.EventTopicMessage, events.PublishKey{Session: "a", Project: "p"}, nil)
		synctest.Wait()
		if runs.Load() != 3 {
			t.Fatal("transcript change did not invalidate preparation")
		}
	})
}

func TestStopBeforeFocus(t *testing.T) {
	s := New(nil)
	s.Stop()
	s.Stop()
	if err := s.Wait(t.Context()); err != nil {
		t.Fatalf("join unused preparation: %v", err)
	}
}

func TestStaleBuildCannotRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int32
		s := New(func(ctx context.Context, id string) (Candidate, error) {
			if id == "a" {
				<-ctx.Done()
			}
			return Candidate{Key: id, Run: func(context.Context) bool { runs.Add(1); return true }}, nil
		})
		defer s.Stop()
		s.Focus(t.Context(), "a", "p")
		synctest.Wait()
		s.Focus(t.Context(), "b", "p")
		synctest.Wait()
		if runs.Load() != 1 {
			t.Fatal("stale candidate reached the runner")
		}
	})
}

func TestBusyEventsSuspendAndDeletionForgetsFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var builds atomic.Int32
		s := New(func(context.Context, string) (Candidate, error) {
			builds.Add(1)
			return Candidate{}, nil
		})
		defer s.Stop()
		s.Focus(t.Context(), "a", "p")
		synctest.Wait()
		s.Observe(api.EventTopicSession, events.PublishKey{}, api.SessionEvent{ID: "a", Status: api.SessionStatusBusy})
		for range 100 {
			s.Changed("a", "p")
		}
		synctest.Wait()
		if builds.Load() != 1 {
			t.Fatal("busy transcript updates rebuilt preparation")
		}
		s.Observe(api.EventTopicSession, events.PublishKey{}, api.SessionEvent{ID: "a", Status: api.SessionStatusIdle})
		synctest.Wait()
		if builds.Load() != 2 {
			t.Fatal("idle transition did not retry preparation")
		}
		s.Observe(api.EventTopicSession, events.PublishKey{}, api.SessionEvent{ID: "a", Action: api.SessionEventActionDeleted})
		s.Changed("", "")
		s.Retry("a")
		synctest.Wait()
		if builds.Load() != 2 {
			t.Fatal("deleted chat retained focus")
		}
	})
}
