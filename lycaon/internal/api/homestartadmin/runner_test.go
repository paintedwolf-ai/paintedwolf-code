package homestartadmin

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/sessionadmin"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/homestart"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// heldPreparation stands in for project preparation, credential indexing
// included, and holds it until released.
type heldPreparation struct {
	entered  chan string
	release  chan struct{}
	finished chan string
}

func (h heldPreparation) PrepareCreatedProject(ctx context.Context, p *project.Project) {
	h.entered <- p.ID
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	h.finished <- p.ID
}

func (heldPreparation) PublishProjectCreated(context.Context, *project.Project) {}

type reservedSessions struct{ sessions store.SessionRepository }

func (r reservedSessions) CreateReservedSession(ctx context.Context, req wire.CreateSessionRequest, reservation sessionadmin.SessionReservation) (*wire.Session, error) {
	return r.sessions.CreateReserved(ctx, reservation.ID, req, req.ProjectID, wire.SessionStatusPreparing, reservation.Commit)
}

func TestHomeStartBindsItsChatWhileProjectPreparationIsHeld(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	fixture := storetest.New(t)
	starts := homestart.NewStore(fixture.DB())
	held := heldPreparation{entered: make(chan string, 1), release: make(chan struct{}), finished: make(chan string, 1)}
	var background taskgroup.Group
	t.Cleanup(func() {
		background.Stop()
		background.Wait(context.Background())
	})
	h := New(&httpio.Responder{}, &background, Deps{
		Store: starts, People: fixture.People, Projects: project.NewSQLRegistry(fixture.DB()),
		ProjectEffects: held, Sessions: reservedSessions{fixture.Sessions}, SessionStore: fixture.Sessions,
		FirstTurns: noFirstTurns{},
	})
	owner, err := fixture.People.HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)
	launchID := uuid.NewString()
	reserved, err := starts.Reserve(t.Context(), launchID, owner.ID, wire.HomeStartChoices{})
	testutil.FailErr(t, "reserve", err)

	h.advance(t.Context(), launchID)

	bound, err := starts.Load(t.Context(), launchID)
	testutil.FailErr(t, "load", err)
	if bound.Progress != homestart.ProgressSessionBound || bound.SessionID != reserved.SessionID {
		t.Fatalf("start = %+v, want its reserved chat bound", bound)
	}
	if got := testutil.Receive(t, "preparation entered", held.entered); got != reserved.ProjectID {
		t.Fatalf("prepared project %s, want %s", got, reserved.ProjectID)
	}
	select {
	case id := <-held.finished:
		t.Fatalf("preparation of %s finished before release", id)
	default:
	}
	close(held.release)
	testutil.Receive(t, "preparation finished", held.finished)
}

type noFirstTurns struct{}

func (noFirstTurns) LockOperation(string) func()                 { return func() {} }
func (noFirstTurns) HoldAdmission(string) func()                 { return func() {} }
func (noFirstTurns) Abort(context.Context, string, string) error { return nil }
