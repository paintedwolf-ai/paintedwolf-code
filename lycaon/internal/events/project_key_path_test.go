package events_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestScopeLookupResolvesRootPath covers root-path event scopes.
func TestScopeLookupResolvesRootPath(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "create project", err)
	lookup := project.ScopeLookup{Registry: reg}
	rootPath := project.PrimaryRootPath(p)

	id, _, claimed, err := lookup.ResolveProject(ctx, rootPath)
	testutil.FailErr(t, "ProjectID by root path", err)
	if id != p.ID || !claimed {
		t.Fatalf("root path resolved to %q, want project id %q", id, p.ID)
	}

	// Unowned folders retain their routing key.
	unowned := t.TempDir()
	id, _, claimed, err = lookup.ResolveProject(ctx, unowned)
	testutil.FailErr(t, "ProjectID for unowned dir", err)
	if id != unowned || claimed {
		t.Fatalf("unowned dir = %q, want it unchanged", id)
	}
}

// TestPathKeyedPublishReachesProjectSubscriber covers path-to-ID routing.
func TestPathKeyedPublishReachesProjectSubscriber(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "create project", err)
	rootPath := project.PrimaryRootPath(p)

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub, Lookup: project.ScopeLookup{Registry: reg}}

	ch, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	pub.PublishProcess(ctx, rootPath, "sess-1", api.BackgroundProcessEvent{ProcessID: "proc-1"})

	select {
	case env := <-ch:
		if env.Topic != api.EventTopicProcess {
			t.Fatalf("topic = %q, want process", env.Topic)
		}
	default:
		t.Fatal("path-keyed event never reached the project-scoped subscriber")
	}
}

// TestPathKeyedPublishStaysOutOfOtherProjects covers project isolation.
func TestPathKeyedPublishStaysOutOfOtherProjects(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	mine, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "create project", err)
	other, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "create other project", err)

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub, Lookup: project.ScopeLookup{Registry: reg}}

	ch, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: other.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	pub.PublishProcess(ctx, project.PrimaryRootPath(mine), "sess-1", api.BackgroundProcessEvent{ProcessID: "proc-1"})

	select {
	case env := <-ch:
		t.Fatalf("another project's event leaked: %+v", env)
	default:
	}
}

// TestResolveProjectDirAcceptsPath covers path-to-root resolution.
func TestResolveProjectDirAcceptsPath(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, t.TempDir())
	testutil.FailErr(t, "create project", err)
	lookup := project.ScopeLookup{Registry: reg}
	rootPath := project.PrimaryRootPath(p)

	_, got, _, err := lookup.ResolveProject(ctx, rootPath)
	testutil.FailErr(t, "resolve root path", err)
	if got != rootPath {
		t.Fatalf("by path = %q want %q", got, rootPath)
	}
}
