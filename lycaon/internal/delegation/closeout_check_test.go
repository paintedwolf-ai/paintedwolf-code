package delegation

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCloseoutCompleteNoDelegation(t *testing.T) {
	store := NewMemoryStore()
	check := CloseoutComplete(store)
	ok, err := check(context.Background(), "sess-1")
	testutil.FailErr(t, "check failed", err)
	if !ok {
		t.Fatal("expected closeout complete with no delegation")
	}
}

func TestCloseoutCompleteActiveDelegation(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	dep, err := store.Create(ctx, api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: "/tmp/p"}, "sess-1", []api.Leg{{}})
	testutil.FailErr(t, "create session in store", err)
	check := CloseoutComplete(store)
	ok, err := check(ctx, "sess-1")
	testutil.FailErr(t, "check failed", err)
	if ok {
		t.Fatal("expected incomplete while delegation active")
	}
	leg := dep.Legs[0]
	leg.Status = api.LegStatusComplete
	testutil.FailErr(t, "complete leg", store.UpdateLeg(ctx, leg))
	_, err = store.Settle(ctx, dep.ID)
	testutil.FailErr(t, "settle delegation", err)

	ok, err = check(ctx, "sess-1")
	testutil.FailErr(t, "check failed", err)
	if !ok {
		t.Fatal("expected complete when delegation done")
	}
}
