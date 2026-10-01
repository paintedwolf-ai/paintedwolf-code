package visual

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type memCoverBinding struct {
	byProject map[string]Cover
}

func (m *memCoverBinding) GetCover(_ context.Context, projectID string) (Cover, bool, error) {
	c, ok := m.byProject[projectID]
	return c, ok, nil
}

func (m *memCoverBinding) SetCover(_ context.Context, projectID string, cover Cover) error {
	if m.byProject == nil {
		m.byProject = map[string]Cover{}
	}
	m.byProject[projectID] = cover
	return nil
}

func TestDesignateCover_latestWins(t *testing.T) {
	store := NewMemoryStore()
	first, err := store.Put(t.Context(), "root-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture},
		Bytes: append(TestPNG1x1Bytes(), 'a'),
	})
	testutil.FailErr(t, "store.Put failed", err)
	second, err := store.Put(t.Context(), "root-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: append(TestPNG1x1Bytes(), 'b'),
	})
	testutil.FailErr(t, "store.Put failed", err)
	binding := &memCoverBinding{}
	lookup := func(_ context.Context, _ string) (string, error) { return "proj-1", nil }

	if err := DesignateCover(context.Background(), store, binding, lookup, DesignateRequest{
		ProjectID: "proj-1", RootSessionID: "root-a", ArtifactID: first.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := DesignateCover(context.Background(), store, binding, lookup, DesignateRequest{
		ProjectID: "proj-1", RootSessionID: "root-a", ArtifactID: second.ID,
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := binding.GetCover(context.Background(), "proj-1")
	if err != nil || !ok {
		t.Fatalf("cover missing: ok=%v err=%v", ok, err)
	}
	if got.ArtifactID != second.ID {
		t.Fatalf("artifact = %s want %s", got.ArtifactID, second.ID)
	}
	if got.Source != api.VisualArtifactSourceRender {
		t.Fatalf("source = %s", got.Source)
	}
}

func TestDesignateCover_foreignProject(t *testing.T) {
	store := NewMemoryStore()
	art, err := store.Put(t.Context(), "root-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture},
		Bytes: TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "store.Put failed", err)
	err = DesignateCover(context.Background(), store, &memCoverBinding{},
		func(_ context.Context, _ string) (string, error) { return "other-proj", nil },
		DesignateRequest{ProjectID: "proj-1", RootSessionID: "root-a", ArtifactID: art.ID},
	)
	if !errors.Is(err, ErrCoverIneligible) {
		t.Fatalf("got %#v want ErrCoverIneligible", err)
	}
}

func TestDesignateCover_missingArtifact(t *testing.T) {
	err := DesignateCover(context.Background(), NewMemoryStore(), &memCoverBinding{},
		func(_ context.Context, _ string) (string, error) { return "proj-1", nil },
		DesignateRequest{ProjectID: "proj-1", RootSessionID: "root-a", ArtifactID: "missing"},
	)
	if !errors.Is(err, ErrCoverNotFound) {
		t.Fatalf("got %#v want ErrCoverNotFound", err)
	}
}

func TestDesignateCover_rejectsUserSource(t *testing.T) {
	store := NewMemoryStore()
	art, err := store.Put(t.Context(), "root-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceUser},
		Bytes: TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "store.Put failed", err)
	err = DesignateCover(context.Background(), store, &memCoverBinding{},
		func(_ context.Context, _ string) (string, error) { return "proj-1", nil },
		DesignateRequest{ProjectID: "proj-1", RootSessionID: "root-a", ArtifactID: art.ID},
	)
	if !errors.Is(err, ErrCoverIneligible) {
		t.Fatalf("got %#v want ErrCoverIneligible", err)
	}
}

func TestDesignateCover_rejectsVideo(t *testing.T) {
	store := NewMemoryStore()
	art, err := store.Put(t.Context(), "root-a", Entry{
		Meta:  api.VisualArtifact{Mime: "video/mp4", Source: api.VisualArtifactSourceCapture},
		Bytes: []byte("mp4"),
	})
	testutil.FailErr(t, "store.Put failed", err)
	err = DesignateCover(context.Background(), store, &memCoverBinding{},
		func(_ context.Context, _ string) (string, error) { return "proj-1", nil },
		DesignateRequest{ProjectID: "proj-1", RootSessionID: "root-a", ArtifactID: art.ID},
	)
	if !errors.Is(err, ErrCoverIneligible) {
		t.Fatalf("got %#v want ErrCoverIneligible", err)
	}
}
