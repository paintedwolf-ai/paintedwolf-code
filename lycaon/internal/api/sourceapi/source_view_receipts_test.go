package sourceapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceCreateReceiptsSurviveViewReleaseAndChurn(t *testing.T) {
	service := &sourceViewService{presentations: pagedview.NewLeaseRegistry[*sourcePresentation](pagedview.NewBudget(8<<20), 8, sourceViewLifetime), registry: pagedview.NewRegistry[*sourceView](pagedview.NewBudget(8<<20), 8, sourceViewLifetime)}
	t.Cleanup(service.close)
	scope := pagedview.Scope{Person: "person", Project: "project"}
	builds := 0
	build := func() *sourceView {
		builds++
		ctx, cancel := context.WithCancel(t.Context())
		return &sourceView{ctx: ctx, cancel: cancel, commands: pagedview.NewCommands[string](&service.receipts)}
	}
	for i := range 600 {
		key := sourceViewCreateKey{scope: scope, client: "window", operation: fmt.Sprint(i)}
		view, release, created, err := service.create(t.Context(), key, []byte("intent"), build)
		testutil.FailErr(t, "create view after churn", err)
		if !created {
			t.Fatal("new request was treated as a replay")
		}
		release()
		service.registry.Release(scope, view.id)
	}
	service.sweep()
	_, _, _, err := service.create(t.Context(), sourceViewCreateKey{scope: scope, client: "window", operation: "0"}, []byte("intent"), build)
	if !errors.Is(err, pagedview.ErrExpired) || builds != 600 {
		t.Fatalf("released retry: builds=%d err=%v", builds, err)
	}
}
