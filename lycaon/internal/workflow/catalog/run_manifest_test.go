package catalog

import (
	"context"
	"errors"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type sessionLookup func(context.Context, string) (*api.Session, error)

func (f sessionLookup) Get(ctx context.Context, id string) (*api.Session, error) { return f(ctx, id) }

func TestProjectDirectoryUsesSessionBeforeFallback(t *testing.T) {
	fallbackCalls := 0
	resolver := Resolver{
		Sessions: sessionLookup(func(_ context.Context, id string) (*api.Session, error) {
			if id != "session-1" {
				t.Fatalf("session lookup = %q", id)
			}
			return &api.Session{WorkspacePath: " /workspace/project "}, nil
		}),
		ProjectDirFallback: func(context.Context, string) (string, error) { fallbackCalls++; return "/fallback", nil },
	}
	if got := resolver.ProjectDirForRun(t.Context(), &api.WorkflowRun{SessionID: " session-1 "}); got != "/workspace/project" {
		t.Fatalf("directory = %q", got)
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback calls = %d", fallbackCalls)
	}
}

func TestProjectDirectoryFallbackHandlesUnavailableSession(t *testing.T) {
	for _, lookup := range []sessionLookup{
		func(context.Context, string) (*api.Session, error) { return nil, errors.New("session unavailable") },
		func(context.Context, string) (*api.Session, error) { return &api.Session{}, nil },
	} {
		resolver := Resolver{Sessions: lookup, ProjectDirFallback: func(_ context.Context, id string) (string, error) {
			if id != "session-1" {
				t.Fatalf("fallback session = %q", id)
			}
			return " /fallback ", nil
		}}
		if got := resolver.ProjectDir(t.Context(), " session-1 "); got != "/fallback" {
			t.Fatalf("directory = %q", got)
		}
		resolver.ProjectDirFallback = func(context.Context, string) (string, error) { return "/unusable", errors.New("failed") }
		if got := resolver.ProjectDir(t.Context(), "session-1"); got != "" {
			t.Fatalf("failed fallback directory = %q", got)
		}
	}
}

func TestEmptyRunHasNoSessionLookup(t *testing.T) {
	resolver := Resolver{Sessions: sessionLookup(func(context.Context, string) (*api.Session, error) {
		t.Fatal("unexpected session lookup")
		return nil, nil
	})}
	if got := resolver.ProjectDirForRun(t.Context(), nil); got != "" {
		t.Fatalf("nil run directory = %q", got)
	}
	if got := resolver.ProjectDir(t.Context(), " "); got != "" {
		t.Fatalf("empty session directory = %q", got)
	}
	if _, err := resolver.ForRun(t.Context(), nil); err == nil {
		t.Fatal("nil run must reject manifest resolution")
	}
}

func TestRunManifestUsesOverlayWithoutMutatingRegistry(t *testing.T) {
	overlayManifest := workflowdef.Manifest{ID: "plan", Version: "1.0.0", Name: "host plan"}
	overlay := workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey("plan", "1.0.0"): overlayManifest})
	resolver := Resolver{Overlay: overlay}
	got, err := resolver.ForRun(t.Context(), &api.WorkflowRun{SessionID: "session-1", WorkflowID: "plan", WorkflowVersion: "1.0.0"})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if got.Name != "host plan" {
		t.Fatalf("resolved name = %q", got.Name)
	}
	merged, err := resolver.Registry(t.Context(), "", "session-1")
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if len(merged.All()) <= len(overlay.All()) {
		t.Fatal("overlay replaced bundled catalog instead of merging")
	}
	if len(overlay.All()) != 1 {
		t.Fatal("merge mutated overlay registry")
	}
	if resolver.SessionScoped(t.Context(), "", "session-1", "plan", "1.0.0") {
		t.Fatal("host overlay must not become session scope")
	}
}
