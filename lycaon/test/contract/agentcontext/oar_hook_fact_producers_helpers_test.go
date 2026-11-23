package contract

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretmint"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

type hookProducerHarness struct {
	rules       *oar.RuleSet
	pipeline    *oar.GuardPipeline
	renderer    *oar.Renderer
	blockPlane  *toolfeedback.BlockPlane
	policy      *policyfacts.Service
	feedback    *policyfeedback.Service
	turnGuards  *turnguards.Service
	declaration *oar.EngineDeclaration
	harvest     *secretharvest.Runtime
	surfaces    *surfaceMapStub
	profiles    *profileMapStub

	advisoryMu sync.Mutex
	advisories []*oar.Decision
}

type surfaceMapStub struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *surfaceMapStub) PromptTurnSurfaceID(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[sessionID]
}

func (s *surfaceMapStub) set(sessionID, surfaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[sessionID] = surfaceID
}

type profileMapStub struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *profileMapStub) PromptToolProfile(_ context.Context, sess *api.Session) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sess != nil {
		if v := p.m[sess.ID]; v != "" {
			return v, nil
		}
		if sess.IsWorkerChild() {
			return "implement", nil
		}
	}
	return "coordinator", nil
}

func (p *profileMapStub) set(sessionID, profileID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[sessionID] = profileID
}

type activeToolsStub struct{}

func (activeToolsStub) Active(string) map[string]bool { return nil }

func newHookProducerHarness(t *testing.T) *hookProducerHarness {
	t.Helper()
	rs, pipeline, renderer, bp := effectiveOARBlockPlane(t)
	declPath := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "oar-profile.yaml")
	decl, err := oar.LoadEngineDeclaration(declPath)
	contractcheck.FailErr(t, "load OAR engine declaration", err)

	inspector, err := secretmint.LoadBundled()
	contractcheck.FailErr(t, "load bundled secretmint", err)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x29}, 32))
	contractcheck.FailErr(t, "new secret fingerprinter", err)
	harvest := secretharvest.NewRuntime(fp)

	surfaces := &surfaceMapStub{m: map[string]string{}}
	profiles := &profileMapStub{m: map[string]string{}}

	fb := policyfeedback.New()
	fb.SetRenderer(renderer)

	policy := policyfacts.New(profiles)
	policy.SetSources(nil, nil, nil, fb, activeToolsStub{})
	policy.SetSurface(surfaces)
	policy.SetPipeline(pipeline)
	policy.SetCredentialSlotProvider(func(context.Context, *api.Session) *secretmint.Inspector { return inspector })
	policy.SetSecretFingerprinter(fp)
	policy.SetHarvestedFingerprint(func(root string, sf secretmatch.SecretFingerprint) bool {
		return harvest.Has(root, sf)
	})

	h := &hookProducerHarness{
		rules:       rs,
		pipeline:    pipeline,
		renderer:    renderer,
		blockPlane:  bp,
		policy:      policy,
		feedback:    fb,
		turnGuards:  &turnguards.Service{ToolPolicy: policy, Feedback: fb},
		declaration: decl,
		harvest:     harvest,
		surfaces:    surfaces,
		profiles:    profiles,
	}
	pipeline.SetAdvisorySink(func(_ context.Context, _ string, _ string, d *oar.Decision) error {
		h.advisoryMu.Lock()
		h.advisories = append(h.advisories, d)
		h.advisoryMu.Unlock()
		return nil
	})
	return h
}

func (h *hookProducerHarness) takeAdvisories() []*oar.Decision {
	h.advisoryMu.Lock()
	defer h.advisoryMu.Unlock()
	out := h.advisories
	h.advisories = nil
	return out
}

type roleCell struct {
	name      string
	role      string
	posture   api.SessionPosture
	mode      api.SessionMode
	surfaceID string
	profileID string
	auth      hoststate.TurnAuthority
	sess      *api.Session
}

func representativeCartesianCells(t *testing.T, h *hookProducerHarness) []roleCell {
	t.Helper()
	coordStates, err := hoststate.CoordinatorStates(t.Context())
	contractcheck.FailErr(t, "CoordinatorStates", err)
	workerStates, err := hoststate.WorkerStates(t.Context())
	contractcheck.FailErr(t, "WorkerStates", err)

	postures := []api.SessionPosture{
		api.SessionPostureSpec,
		api.SessionPostureBuild,
		api.SessionPostureOrchestrate,
		api.SessionPostureVet,
	}
	modes := []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk}

	var cells []roleCell
	for _, posture := range postures {
		for _, mode := range modes {
			var picked *hoststate.CoordinatorState
			for i := range coordStates {
				st := &coordStates[i]
				if st.Excluded || st.Posture != posture || st.SessionMode != mode || st.RootCount == 0 {
					continue
				}
				picked = st
				break
			}
			if picked == nil {
				t.Fatalf("no realizable coordinator state for posture=%s mode=%s", posture, mode)
			}
			auth, fErr := hoststate.FreezeTurnAuthority(t.Context(), *picked)
			contractcheck.FailErr(t, "FreezeTurnAuthority "+picked.ID, fErr)
			sessID := "coord-" + string(posture) + "-" + string(mode)
			surfaceID := picked.TurnProfile.SurfaceID
			h.surfaces.set(sessID, surfaceID)
			h.profiles.set(sessID, "coordinator")
			cells = append(cells, roleCell{
				name:      "coordinator/" + string(posture) + "/" + string(mode),
				role:      "coordinator",
				posture:   posture,
				mode:      mode,
				surfaceID: surfaceID,
				profileID: "coordinator",
				auth:      auth,
				sess: &api.Session{
					ID:        sessID,
					ProjectID: "proj-hook-test",
					Posture:   posture,
					Mode:      mode,
				},
			})

			var pickedWorker *hoststate.WorkerState
			for i := range workerStates {
				ws := &workerStates[i]
				if ws.Excluded || ws.Posture != posture || ws.ParentSessionMode != mode {
					continue
				}
				pickedWorker = ws
				break
			}
			if pickedWorker == nil {
				t.Fatalf("no realizable worker state for posture=%s parentMode=%s", posture, mode)
			}
			wAuth, wErr := hoststate.FreezeWorkerAuthority(t.Context(), *pickedWorker)
			contractcheck.FailErr(t, "FreezeWorkerAuthority "+pickedWorker.AgentID, wErr)
			wSessID := "worker-" + string(posture) + "-" + string(mode)
			h.profiles.set(wSessID, pickedWorker.ToolProfile)
			cells = append(cells, roleCell{
				name:      "worker/" + string(posture) + "/" + string(mode),
				role:      "worker",
				posture:   posture,
				mode:      mode,
				surfaceID: "",
				profileID: pickedWorker.ToolProfile,
				auth:      wAuth,
				sess: &api.Session{
					ID:              wSessID,
					ParentSessionID: sessID,
					ProjectID:       "proj-hook-test",
					Posture:         posture,
					Mode:            mode,
				},
			})
		}
	}
	return cells
}

func (c roleCell) ctx(base context.Context) context.Context {
	ctx := toolreach.WithRequest(base, c.auth.Reach)
	return curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       c.sess.ID,
		ParentSessionID: c.sess.ParentSessionID,
		Posture:         string(c.posture),
		Mode:            string(c.mode),
		Surface:         c.surfaceID,
	})
}
