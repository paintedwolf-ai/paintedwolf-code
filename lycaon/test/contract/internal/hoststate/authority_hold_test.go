package hoststate_test

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// Every authority accessor must reject calls made outside an active
// HoldAuthorityMemos span rather than silently populating an unbounded
// process-wide memo.
func TestAuthorityAccessorsRequireHold(t *testing.T) {
	ctx := t.Context()
	overlays, err := hoststate.DiscoverPackOverlays(ctx)
	contractcheck.FailErr(t, "DiscoverPackOverlays", err)
	stock := overlays[0]

	coordStates, err := hoststate.CoordinatorStates(ctx)
	contractcheck.FailErr(t, "CoordinatorStates", err)
	var coord hoststate.CoordinatorState
	for _, st := range coordStates {
		if !st.Excluded && st.PackOverlayID == "stock" {
			coord = st
			break
		}
	}
	if coord.ID == "" {
		t.Fatal("no realizable stock CoordinatorState found")
	}

	worker := hoststate.WorkerState{
		AgentID:           "implementer",
		ToolProfile:       "implement",
		Posture:           api.SessionPostureBuild,
		BranchView:        hoststate.BranchMainGit,
		RootCount:         1,
		ParentSessionMode: api.SessionModeAgent,
		WebSearch:         true,
		HasGit:            true,
		HasAnyGitRoot:     true,
		Overlay:           stock,
	}

	checks := []struct {
		name string
		call func() error
	}{
		{
			name: "ExecutorForOverlay",
			call: func() error {
				_, _, err := hoststate.ExecutorForOverlay(stock, api.SessionPostureBuild, false, true, false, hoststate.MCPNone)
				return err
			},
		},
		{
			name: "RegistryAndExecutorForOverlay",
			call: func() error {
				_, _, _, err := hoststate.RegistryAndExecutorForOverlay(stock, api.SessionPostureBuild, false, true, false, hoststate.MCPNone)
				return err
			},
		},
		{
			name: "FreezeTurnAuthority",
			call: func() error {
				_, err := hoststate.FreezeTurnAuthority(ctx, coord)
				return err
			},
		},
		{
			name: "FreezeTurnAuthorityWithResources",
			call: func() error {
				_, err := hoststate.FreezeTurnAuthorityWithResources(ctx, coord, toolcontract.ResourcePresence{CommandJobs: true})
				return err
			},
		},
		{
			name: "FreezeWorkerAuthority",
			call: func() error {
				_, err := hoststate.FreezeWorkerAuthority(ctx, worker)
				return err
			},
		},
		{
			name: "WorkerReaderForSession",
			call: func() error {
				_, err := hoststate.WorkerReaderForSession(ctx, coord, "implementer")
				return err
			},
		},
		{
			name: "WorkerStates",
			call: func() error {
				_, err := hoststate.WorkerStates(ctx)
				return err
			},
		},
	}

	for _, tc := range checks {
		if gotErr := tc.call(); !errors.Is(gotErr, hoststate.ErrAuthorityMemosNotHeld) {
			t.Errorf("%s outside hold = %v, want ErrAuthorityMemosNotHeld", tc.name, gotErr)
		}
	}
}

// Nested and concurrent holds keep authority memos usable until the last active
// hold releases, drop them on final release so subsequent unheld calls fail,
// and allow a later hold to freeze identical authorities afresh.
func TestHoldAuthorityMemosLifetimeContract(t *testing.T) {
	ctx := t.Context()
	coordStates, err := hoststate.CoordinatorStates(ctx)
	contractcheck.FailErr(t, "CoordinatorStates", err)
	var coord hoststate.CoordinatorState
	for _, st := range coordStates {
		if !st.Excluded && st.PackOverlayID == "stock" {
			coord = st
			break
		}
	}
	if coord.ID == "" {
		t.Fatal("no realizable stock CoordinatorState found")
	}

	outerRelease := hoststate.HoldAuthorityMemos()
	defer outerRelease()
	innerRelease := hoststate.HoldAuthorityMemos()

	first, err := hoststate.FreezeTurnAuthority(ctx, coord)
	contractcheck.FailErr(t, "first FreezeTurnAuthority", err)
	if first.Digest == "" || len(first.Offered) == 0 || first.Reader.Fingerprint() == "" {
		t.Fatalf("first authority incomplete: digest=%q offered=%d fp=%q", first.Digest, len(first.Offered), first.Reader.Fingerprint())
	}

	// Releasing the inner hold (even twice) must keep the outer hold usable.
	innerRelease()
	innerRelease()

	var (
		wg   sync.WaitGroup
		errs = make(chan error, 8)
	)
	for range 8 {
		wg.Go(func() {
			got, freezeErr := hoststate.FreezeTurnAuthority(ctx, coord)
			if freezeErr != nil {
				errs <- freezeErr
				return
			}
			if got.Digest != first.Digest || !slices.Equal(got.Offered, first.Offered) || got.Reader.Fingerprint() != first.Reader.Fingerprint() {
				errs <- errors.New("concurrent authority diverged from first freeze")
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		contractcheck.FailErr(t, "concurrent FreezeTurnAuthority under outer hold", err)
	}

	// Releasing the final hold drops the store; unheld accessors fail again.
	outerRelease()
	if _, err := hoststate.FreezeTurnAuthority(ctx, coord); !errors.Is(err, hoststate.ErrAuthorityMemosNotHeld) {
		t.Fatalf("FreezeTurnAuthority after final release = %v, want ErrAuthorityMemosNotHeld", err)
	}

	// A subsequent hold repopulates cleanly with identical results.
	secondRelease := hoststate.HoldAuthorityMemos()
	defer secondRelease()
	reacquired, err := hoststate.FreezeTurnAuthority(ctx, coord)
	contractcheck.FailErr(t, "reacquired FreezeTurnAuthority", err)
	if reacquired.Digest != first.Digest || !slices.Equal(reacquired.Offered, first.Offered) || reacquired.Reader.Fingerprint() != first.Reader.Fingerprint() {
		t.Fatalf("reacquired authority diverged: digest=%q/%q fp=%q/%q",
			reacquired.Digest, first.Digest, reacquired.Reader.Fingerprint(), first.Reader.Fingerprint())
	}
}
