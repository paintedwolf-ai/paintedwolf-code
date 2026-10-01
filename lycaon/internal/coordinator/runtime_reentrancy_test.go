package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestBuildCompletionMessagesAllowsReentryFromAssembly checks the Runtime lock boundary.
func TestBuildCompletionMessagesAllowsReentryFromAssembly(t *testing.T) {
	rt := NewRuntime(RuntimeDeps{})

	reentered := make(chan struct{}, 1)
	rt.assembler = &reentrantAssemblyProbe{
		onBuild: func() {
			_ = rt.CoordinatorLoop()
			reentered <- struct{}{}
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := rt.BuildCompletionMessages(context.Background(), &api.Session{ID: "s1"}, nil, nil)
		done <- err
	}()

	select {
	case <-reentered:
	case <-time.After(1 * time.Second):
		t.Fatal("BuildCompletionMessages blocked Runtime.CoordinatorLoop re-entry")
	}

	select {
	case err := <-done:
		testutil.FailErr(t, "BuildCompletionMessages", err)
	case <-time.After(1 * time.Second):
		t.Fatal("BuildCompletionMessages did not return after re-entry")
	}
}

// reentrantAssemblyProbe re-enters Runtime during completion assembly.
type reentrantAssemblyProbe struct {
	onBuild func()
}

func (p *reentrantAssemblyProbe) BuildCompletionMessages(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
	if p.onBuild != nil {
		p.onBuild()
	}
	return history, nil
}

func (p *reentrantAssemblyProbe) BeginPromptTurn(string, ...string) {}

func (p *reentrantAssemblyProbe) EndPromptTurn(string) {}

func (p *reentrantAssemblyProbe) SetTurnSurfaceID(string, string) {}

func (p *reentrantAssemblyProbe) TurnSurfaceID(string) string { return "" }

// TestRuntimeLockFreeAccessorsRemainReentrant checks access while depsMu is held.
func TestRuntimeLockFreeAccessorsRemainReentrant(t *testing.T) {
	rt := NewRuntime(RuntimeDeps{})

	// Hold depsMu while exercising lock-free accessors.
	released := make(chan struct{})
	holding := make(chan struct{})
	go func() {
		rt.depsMu.Lock()
		close(holding)
		<-released
		rt.depsMu.Unlock()
	}()
	<-holding
	t.Cleanup(func() { close(released) })

	type call struct {
		name string
		fn   func()
	}
	cases := []call{
		{"Kicks", func() { _ = rt.Kicks() }},
		{"Board", func() { _ = rt.Board() }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				c.fn()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatalf("%s blocked on depsMu", c.name)
			}
		})
	}
}
