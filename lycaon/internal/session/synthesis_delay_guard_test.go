package session

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newSynthesisDelayManager(t *testing.T) (*Host, *api.Session) {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	store := store.NewMemory()

	sess, err := store.Create(context.Background(), api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create coordinator session", err)
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetProgressStore(progress.NewMemoryStore())
	mgr.SetRejectFormatter(rejectFmt)
	schemaDir := filepath.Join("..", "..", "..", "schemas")
	loader, err := oar.NewLoader(schemaDir)
	testutil.FailErr(t, "oar.NewLoader", err)
	rs, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "oar.LoadStock", err)
	pipeline := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	pipeline.EnableAnchor(oar.AnchorCoordinatorPreInvoke)
	pipeline.EnableAnchor(oar.AnchorWorkerReportCheck)
	mgr.SetOARPipeline(pipeline, oar.NewRenderer(rejectFmt, synthesisDelayNudge{f: rejectFmt}))
	return mgr, sess
}

type synthesisDelayNudge struct {
	f *guidance.StaticRejectFormatter
}

func (n synthesisDelayNudge) FormatNudge(ctx context.Context, code string, data map[string]any) (string, error) {
	return guidance.FormatCoordinatorNudge(ctx, n.f, code, data)
}

func rejectCloseout(t *testing.T, mgr *Host, sess *api.Session, surfaceID string, workersIdle bool) (string, bool) {
	t.Helper()
	reject, blocked := mgr.Coordinator.Guards.OpenProgress(context.Background(), sess, nil, surfaceID, workersIdle, surface.ImplementSessionState{}, true)
	return reject.Error(), blocked
}

func TestCloseoutSkipsOpenProgressWhenInvokeGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [ ] review")

	if _, block := mgr.Coordinator.Guards.OpenProgress(
		context.Background(), sess, nil, "implement_investigate", true, surface.ImplementSessionState{}, false,
	); block {
		t.Fatal("expected no open-progress hold when invokeAllowed=false")
	}
}

func TestCloseoutBlocksOnOpenSteps(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [x] survey\n- [ ] review")

	reject, block := rejectCloseout(t, mgr, sess, "implement_investigate", true)
	if !block {
		t.Fatal("expected the closeout to be held back while a step is open")
	}
	if !strings.Contains(reject, "PROGRESS_OPEN_BEFORE_CLOSEOUT") {
		t.Fatalf("reject should carry the hint code, got %q", reject)
	}
}

func TestCloseoutAllowsWhenTerminal(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	// Done, skipped, and optional steps do not block closeout.
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [x] survey\n- [~] review\n- [>] synthesize report")

	if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", true); block {
		t.Fatal("a terminal plan (done/na/optional) must let the closeout through")
	}
}

func TestCloseoutSkipsReadOnlySynthesisAndNonProseSurfaces(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [ ] review")

	if _, block := rejectCloseout(t, mgr, sess, "implement_synthesis", true); block {
		t.Fatal("the read-only synthesis surface must never carry the open-plan push")
	}
	// Dispatch ends through a tool call.
	if _, block := rejectCloseout(t, mgr, sess, "implement_dispatch", true); block {
		t.Fatal("non-prose surfaces must not carry the open-plan push")
	}
	if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", false); block {
		t.Fatal("the push must not apply while workers are still in flight")
	}
}

func TestCloseoutBoundedPerPrompt(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.RewindRuntime.Progress.Set(sess.ID, "## Progress\n- [ ] review")
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", true); !block {
			t.Fatalf("delay %d should still block", i)
		}
	}
	if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", true); block {
		t.Fatal("the closeout must be let through once the per-prompt delay cap is reached")
	}
	mgr.Runner.Closeouts.BeginPrompt(ctx, sess, promptinput.Input{Text: "continue"})
	if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", true); !block {
		t.Fatal("reset should re-arm the push for the next prompt")
	}
}
