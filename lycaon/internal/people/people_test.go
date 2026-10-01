package people_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOwnerHoldsEveryAuthority(t *testing.T) {
	owner := people.Person{ID: "person-1", Role: api.PersonRoleOwner}
	if !people.MayInvoke(owner, "getHost") || !people.MayObserveEvents(owner) {
		t.Fatal("owner lacks authority")
	}
}

func TestUnknownOrMissingPersonsHoldNoAuthority(t *testing.T) {
	for _, p := range []people.Person{{}, {ID: "person-1"}, {ID: "person-1", Role: api.PersonRole("unrecognized")}} {
		if people.MayInvoke(p, "getHost") || people.MayObserveEvents(p) {
			t.Fatalf("%+v holds authority", p)
		}
	}
	if people.MayInvoke(people.Person{ID: "person-1", Role: api.PersonRoleOwner}, "") {
		t.Fatal("owner may invoke an unnamed operation")
	}
}

func TestCallerRoundTripsAndRejectsIncompletePersons(t *testing.T) {
	owner := people.Person{ID: "person-1", Role: api.PersonRoleOwner}
	if got, ok := people.Caller(people.WithCaller(context.Background(), owner)); !ok || got != owner {
		t.Fatalf("caller=%+v ok=%v", got, ok)
	}
	if _, ok := people.Caller(people.WithCaller(context.Background(), people.Person{Role: api.PersonRoleOwner})); ok {
		t.Fatal("person without id bound as caller")
	}
	if _, ok := people.Caller(context.Background()); ok {
		t.Fatal("empty context has a caller")
	}
}

type fixedOwner struct {
	owner people.Person
	err   error
}

func (f fixedOwner) HostOwner(context.Context) (people.Person, error) { return f.owner, f.err }

func TestActingPrefersTheCallerOverTheHostOwner(t *testing.T) {
	owner := people.Person{ID: "owner", Role: api.PersonRoleOwner}
	caller := people.Person{ID: "caller", Role: api.PersonRoleOwner}
	got, err := people.Acting(people.WithCaller(context.Background(), caller), fixedOwner{owner: owner})
	testutil.FailErr(t, "acting with caller", err)
	if got != caller {
		t.Fatalf("acting=%+v want caller", got)
	}
	got, err = people.Acting(context.Background(), fixedOwner{owner: owner})
	testutil.FailErr(t, "acting without caller", err)
	if got != owner {
		t.Fatalf("acting=%+v want host owner", got)
	}
	wantErr := errors.New("store closed")
	if _, err := people.Acting(context.Background(), fixedOwner{err: wantErr}); !errors.Is(err, wantErr) {
		t.Fatalf("acting err=%v want %v", err, wantErr)
	}
}

func TestDecidingNamesOnlyTheBoundCaller(t *testing.T) {
	caller := people.Person{ID: "caller", Role: api.PersonRoleOwner}
	got, err := people.Deciding(people.WithCaller(context.Background(), caller))
	testutil.FailErr(t, "deciding with caller", err)
	if got != caller {
		t.Fatalf("deciding=%+v want caller", got)
	}
	if _, err := people.Deciding(context.Background()); !errors.Is(err, people.ErrNoDecidingPerson) {
		t.Fatalf("deciding without caller err=%v want %v", err, people.ErrNoDecidingPerson)
	}
}

func TestWithoutCallerDetachesAgentWorkFromTheRequester(t *testing.T) {
	owner := people.Person{ID: "owner", Role: api.PersonRoleOwner}
	caller := people.Person{ID: "caller", Role: api.PersonRoleOwner}
	ctx := people.WithoutCaller(people.WithCaller(context.Background(), caller))
	if _, ok := people.Caller(ctx); ok {
		t.Fatal("caller survived WithoutCaller")
	}
	if _, err := people.Deciding(ctx); !errors.Is(err, people.ErrNoDecidingPerson) {
		t.Fatalf("deciding after WithoutCaller err=%v", err)
	}
	got, err := people.Acting(ctx, fixedOwner{owner: owner})
	testutil.FailErr(t, "acting after WithoutCaller", err)
	if got != owner {
		t.Fatalf("acting=%+v want host owner", got)
	}
}
