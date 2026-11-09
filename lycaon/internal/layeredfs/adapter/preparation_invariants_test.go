package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Only capability discovery is substituted; Choose and its cost survey remain real.
type preparationProbe struct {
	Adapter
	id     AdapterID
	reason ReasonCode
}

func (p preparationProbe) ID() AdapterID { return p.id }

func (p preparationProbe) Probe(context.Context, ProbeConfig) (ProbeResult, error) {
	return ProbeResult{AdapterID: p.id, Supported: p.reason == ReasonNone, ReasonCode: p.reason}, nil
}

func preparationSelector(platform string, reason ReasonCode, absent bool) *Selector {
	var mount Adapter
	id := AdapterFUSELinux
	if platform == "darwin" {
		id = AdapterNFSDarwin
	}
	if !absent {
		mount = preparationProbe{id: id, reason: reason}
	}
	s := NewSelector(platform, mount, mount, nil)
	s.BudgetFor = func(class BudgetClass) BudgetFact {
		return BudgetFact{Class: class, Limit: 4, EntryCost: 1}
	}
	return s
}

func TestPreparationInvariantCapabilityLossCannotAuthorizeMoreWork(t *testing.T) {
	source := t.TempDir()
	for i := range 3 {
		testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(source, fmt.Sprint(i)), []byte("x"), 0o600))
	}
	outcomes := []struct {
		name   string
		reason ReasonCode
		absent bool
	}{
		{name: "available"},
		{name: "absent", absent: true},
		{name: "denied", reason: ReasonMountDenied},
		{name: "confinement", reason: ReasonConfinementUnavailable},
		{name: "transient", reason: ReasonTransientStartupFailure},
	}
	for _, platform := range []string{"linux", "darwin"} {
		for _, clone := range []bool{false, true} {
			for _, indexed := range []bool{false, true} {
				for _, outcome := range outcomes {
					t.Run(fmt.Sprintf("%s/clone=%v/index=%v/%s", platform, clone, indexed, outcome.name), func(t *testing.T) {
						grant := RootGrant{RootID: "r", SourcePath: source, IsPrimary: true}
						if indexed {
							grant.Estimate = &SourceIndexEstimate{EstimatedEntries: 3}
						}
						s := preparationSelector(platform, outcome.reason, outcome.absent)
						choice, err := s.Choose(t.Context(), ChooseRequest{
							Grants: []RootGrant{grant},
							CloneAvailable: func(context.Context, RootGrant) (bool, error) { return clone, nil },
						})
						if outcome.name == "available" {
							testutil.FailErr(t, "choose mounted provider", err)
							if choice.Adapter == nil || choice.Adapter.ID() == AdapterEagerCopy {
								t.Fatalf("over-limit source must mount: %+v", choice)
							}
							return
						}
						if err == nil || choice.Adapter != nil {
							t.Fatalf("lost mount capability authorized over-limit work: choice=%+v err=%v", choice, err)
						}
					})
				}
			}
		}
	}
}

func TestPreparationInvariantOverrunNeverResurveysLowerTree(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			s := preparationSelector(platform, ReasonMountDenied, false)
			// The overrun already proves materialization exceeds the limit. Reopening
			// the unavailable lower tree cannot make that decision more authoritative.
			choice, err := s.Choose(t.Context(), ChooseRequest{
				Grants: []RootGrant{{RootID: "r", SourcePath: filepath.Join(t.TempDir(), "unavailable")}},
				Overrun: &BudgetOverrunError{RootID: "r", Entries: 3, Limit: 4},
			})
			if err == nil || choice.Adapter != nil {
				t.Fatalf("overrun must refuse when mounting fails: choice=%+v err=%v", choice, err)
			}
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				t.Fatalf("overrun triggered a new lower-tree survey: %v", err)
			}
			if choice.Probe.ReasonCode != ReasonMountDenied {
				t.Fatalf("mount failure must remain diagnosable: %+v", choice.Probe)
			}
		})
	}
}

func TestPreparationInvariantBoundaryKeepsCopyBudget(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			s := preparationSelector(platform, ReasonMountDenied, true)
			choice, err := s.Choose(t.Context(), ChooseRequest{
				Grants: []RootGrant{{RootID: "r", SourcePath: t.TempDir(), Estimate: &SourceIndexEstimate{EstimatedEntries: 2}}},
				CloneAvailable: func(context.Context, RootGrant) (bool, error) { return true, nil },
			})
			testutil.FailErr(t, "choose at limit", err)
			if choice.Adapter == nil || choice.Adapter.ID() != AdapterEagerCopy || choice.CopyBudget == nil {
				t.Fatalf("cheap preparation must retain live overrun enforcement even without a mount: %+v", choice)
			}
		})
	}
}
