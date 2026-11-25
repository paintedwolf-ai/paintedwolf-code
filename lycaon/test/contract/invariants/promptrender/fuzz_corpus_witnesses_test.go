package promptrender_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

// The committed fuzz corpus found three defects. Each stays a regression by
// what it names, built from production domain members, so a codec change that
// moves a corpus byte to another row keeps the named condition and outcome.
func TestFuzzCorpusSemanticWitnesses(t *testing.T) {
	env, err := loadFuzzEnvOnce()
	if err != nil {
		t.Fatalf("loadFuzzEnvOnce: %v", err)
	}
	d := env.domains
	run := func(t *testing.T, seed []byte) FuzzCaseReport {
		t.Helper()
		rep, err := executeFuzzRecipientCase(t.Context(), env, seed, t.TempDir())
		if err != nil || !rep.PropertyExecuted {
			t.Fatalf("rep=%+v err=%v", rep, err)
		}
		return rep
	}

	// 7a8c30225a082d3d: worker personas with web search off, which withholds
	// the web tools.
	t.Run("worker_personas_with_web_search_disabled", func(t *testing.T) {
		for _, agent := range []string{"implementer", "web-researcher", "security-reviewer", "repo-researcher"} {
			seed := hoststate.WorkerSeed{
				OverlayIndex: overlayIndexByID(t, d, "stock"), AgentID: agent, Posture: api.SessionPostureBuild,
				BranchView: hoststate.BranchMainGit, RootCount: 1, Loading: hoststate.WorkerLoadingAllLoaded,
				ParentSessionMode: api.SessionModeAgent, ApprovalPosture: gate.PostureBalanced, TempState: hoststate.TempNone,
			}
			if agent == "implementer" {
				seed.BranchView = hoststate.BranchIsolatedWorker
			}
			ws, err := hoststate.ResolveWorkerSeed(d, seed)
			if err != nil || ws.WebSearch {
				t.Fatalf("%s: resolved %s err=%v, want a web-off worker", agent, ws.ID, err)
			}
			raw, err := d.EncodeWorker(seed, hoststate.SeedTail{})
			if err != nil {
				t.Fatalf("%s: encode: %v", agent, err)
			}
			if !ws.Excluded {
				run(t, raw)
				continue
			}
			// Production withholds this agent without web search; the case
			// reports that exclusion with its production reason.
			rep, err := executeFuzzRecipientCase(t.Context(), env, raw, t.TempDir())
			if err != nil || !rep.Excluded || rep.ExcludedReason == "" {
				t.Fatalf("%s: rep=%+v err=%v, want an exclusion with a production reason", agent, rep, err)
			}
		}
	})

	// 028e1c4e90279a32: every surface a coordinator whose catalog has the
	// implement pack removed is admitted to still receives true guidance,
	// including its spawn roster.
	t.Run("coordinator_without_the_implement_pack", func(t *testing.T) {
		admitted := 0
		for _, surf := range d.Surfaces {
			seed := hoststate.CoordinatorSeed{
				OverlayIndex: overlayIndexByID(t, d, "without-implement"), SessionMode: api.SessionModeAgent,
				Posture: api.SessionPostureBuild, WorkflowState: hoststate.WorkflowAmbientIdle, SurfaceID: surf.ID,
				RootTopology: hoststate.RootsSingleGit, Loading: hoststate.LoadingAllLoaded, WebSearch: true,
				ApprovalPosture: gate.PostureBalanced, TempState: hoststate.TempScratchOnly, MCPState: hoststate.MCPNone,
			}
			st, err := hoststate.ResolveCoordinatorSeed(d, seed)
			if err != nil || st.PackOverlayID != "without-implement" {
				t.Fatalf("resolved %s err=%v, want a coordinator without the implement pack", st.ID, err)
			}
			if st.Excluded {
				continue
			}
			admitted++
			raw, err := d.EncodeCoordinator(seed, hoststate.SeedTail{})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			run(t, raw)
		}
		if admitted == 0 {
			t.Fatal("no surface admits a coordinator without the implement pack")
		}
	})

	// build_git_to_spec_nongit: the masked two-axis interaction, a live job
	// across a turn whose posture and primary-root Git both change.
	t.Run("posture_and_primary_git_change_together", func(t *testing.T) {
		spec := slices.IndexFunc(d.Postures, func(p sessionprofiles.PostureSpec) bool { return p.ID == api.SessionPostureSpec })
		nonGit := slices.Index(d.RootTopologies, hoststate.RootsSingleNonGit)
		if spec < 0 || nonGit < 0 {
			t.Fatal("spec posture or single non-Git topology missing from the seed domains")
		}
		raw := encodeCoordinatorFuzzBytes(
			t, d, "stock", api.SessionPostureBuild, api.SessionModeAgent, "implement_investigate",
			hoststate.RootsSingleGit, hoststate.LoadingAllLoaded, hoststate.WorkflowActivePhase,
			gate.PostureBalanced, hoststate.TempScratchOnly, hoststate.MCPNone,
			0, 0, 2, 1, uint8(spec), 3, uint8(nonGit),
		)
		rep := run(t, raw)
		decoded, err := d.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		b := decoded.Coordinator
		for _, ch := range decoded.Tail.Changes[:2] {
			b = mutateCoordinatorSeed(d, b, ch.Axis, ch.Value)
		}
		stB, err := hoststate.ResolveCoordinatorSeed(d, b)
		if err != nil || stB.Posture != api.SessionPostureSpec || stB.HasGit {
			t.Fatalf("turn B %s (rep B %s) err=%v, want spec posture without primary-root Git", stB.ID, rep.TurnBCoords, err)
		}
	})
}
