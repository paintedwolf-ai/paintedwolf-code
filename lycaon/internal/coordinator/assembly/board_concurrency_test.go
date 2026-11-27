package assembly_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBoardEngineConcurrentWakePrediction(t *testing.T) {
	engine := assembly.NewBoardEngine(invalidateStubBuilder{}, invalidateStubFormatter{}, nil)
	ctx := t.Context()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	reader, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  profiles,
		RootCount: 1,
		WebSearch: true,
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		Declared:  true,
	})
	testutil.FailErr(t, "NewReader coordinator", err)
	sess := &api.Session{ID: "s1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	run := api.CoordinatorRunContext{RunID: "initial", CurrentPhase: "boot"}
	if _, ok := engine.PrependBoardIfChanged(ctx, sess, reader, run); !ok {
		t.Fatal("initial board was not injected")
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := range 1000 {
			next := api.CoordinatorRunContext{RunID: strconv.Itoa(i), CurrentPhase: "boot"}
			engine.PrependBoardIfChanged(ctx, sess, reader, next)
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for range 1000 {
			engine.BoardWillForceInject(ctx, sess, run)
			engine.BoardInjectHash(sess.ID)
		}
	}()
	close(start)
	workers.Wait()
	last := api.CoordinatorRunContext{RunID: "999", CurrentPhase: "boot"}
	if engine.BoardWillForceInject(ctx, sess, last) {
		t.Fatal("final run did not retain a stable board snapshot")
	}
	if engine.BoardInjectHash(sess.ID) == "" {
		t.Fatal("final run lost its board cache key")
	}
}
