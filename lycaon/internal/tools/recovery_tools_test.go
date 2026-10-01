package tools

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

func TestRecoveryCopyUsesFrozenOfferedTools(t *testing.T) {
	bp := stockBlockPlane(t)
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{{ID: "fixture", Tools: map[string]bool{"read": true, "list_dir": true, "grep": true}}}, toolvocab.Provenance{})
	testutil.FailErr(t, "load recovery vocabulary", err)
	for _, tc := range []struct {
		name    string
		offered []string
		listing bool
	}{
		{"listing offered", []string{"read", "grep", "list_dir"}, true},
		{"narrow profile", []string{"read", "grep"}, false},
		{"no tools offered", []string{}, false},
		{"non-model caller", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.offered != nil {
				ctx = WithRecoveryTools(ctx, tc.offered)
			}
			// Producer metadata cannot invent an offered recovery tool.
			err := bp.RejectObservation(ctx, "read", "plan_write_only", nil, &ToolReject{
				Code: "READ_IS_DIRECTORY", Data: map[string]any{"path": "plans", "can_list_dir": true},
			})
			if err == nil {
				t.Fatal("directory rejection missing")
			}
			refusal, ok := guidance.RefusalFromError(err)
			if !ok || refusal.Copy == nil {
				t.Fatalf("missing evaluated recovery: %v", err)
			}
			calls := toolvocab.NamedTools(catalog, refusal.Copy["fix"])
			if got := slices.Contains(calls, "list_dir"); got != tc.listing {
				t.Fatalf("listing recommendation = %v, want %v: %v", got, tc.listing, calls)
			}
			for _, call := range calls {
				if !slices.Contains(tc.offered, call) {
					t.Errorf("recovery names unoffered tool %q", call)
				}
			}
		})
	}
}

func TestRecoveryFactsAreLazyAndKeepTheirRequestSnapshot(t *testing.T) {
	names := []string{"read", "list_dir"}
	ctx := WithRecoveryTools(t.Context(), names)
	names[1] = "task"
	gc := oar.NewGuardContext()
	RegisterRecoveryFacts(ctx, gc)
	if len(gc.ObservationData) != 0 {
		t.Fatal("recovery facts materialized before a rule referenced them")
	}
	testutil.FailErr(t, "publish selected recovery fact", gc.Ensure("paintedwolf.can_list_dir"))
	if gc.ObservationData["can_list_dir"] != true {
		t.Fatal("recovery used a later tool list")
	}
	if _, exists := gc.ObservationData["can_read"]; exists {
		t.Fatal("unreferenced recovery fact was materialized")
	}
}

func TestLocalFetchRecoveryOnlyOffersCurrentCaptureTools(t *testing.T) {
	bp := stockBlockPlane(t)
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{{ID: "fixture", Tools: map[string]bool{"capture_page": true, "measure_page": true}}}, toolvocab.Provenance{})
	testutil.FailErr(t, "load browser recovery vocabulary", err)
	for _, offered := range [][]string{nil, {"capture_page"}, {"measure_page"}, {"capture_page", "measure_page"}} {
		ctx := WithRecoveryTools(t.Context(), offered)
		err := bp.RejectObservation(ctx, "fetch_url", "fixture", nil, &ToolReject{Code: "FETCH_URL_BLOCKED", Data: map[string]any{"detail": "private address", "can_capture_page": true, "can_measure_page": true}})
		refusal, ok := guidance.RefusalFromError(err)
		if !ok || refusal.Copy == nil {
			t.Fatalf("missing refusal: %v", err)
		}
		calls := toolvocab.NamedTools(catalog, refusal.Copy["fix"])
		for _, call := range calls {
			if !slices.Contains(offered, call) {
				t.Fatalf("recovery invented %s with offered %v", call, offered)
			}
		}
		if len(offered) > 0 && len(calls) == 0 {
			t.Fatalf("offered local recovery omitted for %v", offered)
		}
	}
}
