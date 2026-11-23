package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

type callAdmissionKey struct{}

// callAdmission carries call-scoped capacity policy into the decorated provider chain
// so preparation and secret screening run outside inference capacity.
type callAdmission struct {
	plane        *UtilityPlane
	capacityKey  string
	singleFlight bool
	foreground   bool
	class        UtilityClass
	preemptable  bool

	dispatched atomic.Bool
	preempted  atomic.Bool
}

func withCallAdmission(ctx context.Context, adm *callAdmission) context.Context {
	if adm == nil {
		return ctx
	}
	return context.WithValue(ctx, callAdmissionKey{}, adm)
}

func callAdmissionFrom(ctx context.Context) *callAdmission {
	adm, _ := ctx.Value(callAdmissionKey{}).(*callAdmission)
	return adm
}

func (a *callAdmission) markDispatched() {
	if a != nil {
		a.dispatched.Store(true)
	}
}

func (a *callAdmission) Dispatched() bool {
	return a != nil && a.dispatched.Load()
}

func (a *callAdmission) PreemptErr() error {
	if a != nil && a.preempted.Load() {
		return context.Canceled
	}
	return nil
}

func isRegistryDecorated(p modelcall.Provider) bool {
	_, ok := p.(*lifecycleProvider)
	return ok
}

type secretScreenedProvider struct {
	inner              modelcall.Provider
	destination        ScreenDestination
	resolveDestination func() ScreenDestination
	screen             OutboundSecretScreen
	registry           *Registry
}

func (p *secretScreenedProvider) ID() string                       { return p.inner.ID() }
func (p *secretScreenedProvider) Models() []modelcall.ModelInfo    { return p.inner.Models() }
func (p *secretScreenedProvider) Profile() providerprofile.Profile { return p.inner.Profile() }

func (p *secretScreenedProvider) currentDestination() ScreenDestination {
	if p.resolveDestination != nil {
		return p.resolveDestination()
	}
	return p.destination
}

func (p *secretScreenedProvider) currentPolicyRevision(ctx context.Context, projectDir string) (string, bool) {
	if overrides, frozen := thinkingPolicyFromContext(ctx); frozen {
		sum := sha256SumJSON(overrides)
		return "frozen:" + hex.EncodeToString(sum[:8]), true
	}
	if p.registry == nil {
		return "", true
	}
	store := p.registry.thinkingPolicy.Load()
	if store == nil {
		return "", true
	}
	policy, err := store.Get(SettingsScopeProject, projectDir)
	if err != nil {
		return "", false
	}
	sum := sha256SumJSON(policy.ThinkingOverrides)
	return "store:" + hex.EncodeToString(sum[:8]), true
}

func (p *secretScreenedProvider) screenOnce(ctx context.Context, baseReq modelcall.CompletionRequest, reprepared bool) (modelcall.CompletionRequest, screenBasis, error) {
	started := time.Now()
	dest := p.currentDestination()
	prepared := baseReq
	if reprepared && p.registry != nil {
		var err error
		prepared, err = (&thinkingPolicyProvider{Provider: p.inner, registry: p.registry}).prepare(ctx, cloneCompletionRequest(baseReq), nil)
		if err != nil {
			lifecycleFromContext(ctx).record(ctx, "screened", "screen_ms", time.Since(started).Milliseconds(), "failed", true)
			return modelcall.CompletionRequest{}, screenBasis{}, err
		}
	}
	policyRev, ok := p.currentPolicyRevision(ctx, prepared.Debug.ProjectDir)
	if !ok {
		policyRev = "invalid"
	}
	var screened modelcall.CompletionRequest
	var basis screenBasis
	var err error
	if revScreen, ok := p.screen.(revalidatingSecretScreen); ok {
		screened, basis, err = revScreen.ScreenWithBasis(ctx, dest, policyRev, prepared)
	} else {
		screened, err = p.screen.Screen(ctx, dest, prepared)
	}
	lifecycleFromContext(ctx).record(ctx, "screened", "screen_ms", time.Since(started).Milliseconds(), "failed", err != nil)
	return screened, basis, err
}

func (p *secretScreenedProvider) screenAndAdmit(ctx context.Context, req modelcall.CompletionRequest) (context.Context, modelcall.CompletionRequest, func(), error) {
	original := cloneCompletionRequest(req)
	screened, basis, err := p.screenOnce(ctx, original, false)
	if err != nil {
		return nil, modelcall.CompletionRequest{}, nil, err
	}
	adm := callAdmissionFrom(ctx)
	if adm == nil || adm.plane == nil || adm.plane.lanes == nil || !adm.singleFlight || adm.capacityKey == "" {
		return ctx, screened, func() {}, nil
	}

	waitCtx := ctx
	var cancelOverlay context.CancelFunc
	if !adm.foreground && adm.class == UtilityClassOverlay {
		waitCtx, cancelOverlay = context.WithTimeout(ctx, adm.plane.lanes.overlayWaitOrDefault())
		defer cancelOverlay()
	}

	admittedCtx := ctx
	var rawCancel context.CancelFunc
	var preemptCancel context.CancelFunc
	if !adm.foreground && adm.preemptable {
		admittedCtx, rawCancel = context.WithCancel(ctx)
		preemptCancel = func() {
			if ctx.Err() == nil {
				adm.preempted.Store(true)
			}
			rawCancel()
		}
	}

	checkFresh := func() bool {
		revScreen, ok := p.screen.(revalidatingSecretScreen)
		if !ok {
			return false
		}
		dest := p.currentDestination()
		policyRev, revOK := p.currentPolicyRevision(ctx, original.Debug.ProjectDir)
		if !revOK {
			return false
		}
		return revScreen.FreshBasis(ctx, dest, policyRev, original, basis)
	}
	rescreen := func() error {
		var rescreenErr error
		screened, basis, rescreenErr = p.screenOnce(ctx, original, true)
		return rescreenErr
	}

	queueStart := time.Now()
	release, waited, rescreened, admitErr := adm.plane.lanes.admitWithRevalidate(
		waitCtx, adm.capacityKey, adm.singleFlight, adm.foreground, preemptCancel, checkFresh, rescreen,
	)
	if admitErr != nil {
		if rawCancel != nil {
			rawCancel()
		}
		if ctx.Err() == nil && !adm.foreground && adm.class == UtilityClassOverlay && waitCtx.Err() != nil {
			admitErr = ErrLiteBusy
		}
		lifecycleFromContext(ctx).record(ctx, "admitted",
			"queue_ms", time.Since(queueStart).Milliseconds(), "contended", waited, "rescreened", rescreened, "failed", true)
		return nil, modelcall.CompletionRequest{}, nil, admitErr
	}
	lifecycleFromContext(ctx).record(ctx, "admitted",
		"queue_ms", time.Since(queueStart).Milliseconds(), "contended", waited, "rescreened", rescreened, "failed", false)
	fullRelease := release
	if rawCancel != nil {
		fullRelease = func() {
			rawCancel()
			release()
		}
	}
	return admittedCtx, screened, fullRelease, nil
}

func (p *secretScreenedProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	admittedCtx, screened, release, err := p.screenAndAdmit(ctx, req)
	if err != nil {
		return nil, err
	}
	defer release()
	return p.inner.Complete(admittedCtx, screened)
}

func (p *secretScreenedProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	admittedCtx, screened, release, err := p.screenAndAdmit(ctx, req)
	if err != nil {
		return nil, err
	}
	ch, err := p.inner.Stream(admittedCtx, screened)
	if err != nil {
		release()
		return nil, err
	}
	return releaseAfterStream(ctx, ch, release), nil
}

func cloneCompletionRequest(req modelcall.CompletionRequest) modelcall.CompletionRequest {
	out := req
	if len(req.Messages) > 0 {
		out.Messages = append([]api.Message(nil), req.Messages...)
	}
	if req.ThinkingOverride != nil {
		v := *req.ThinkingOverride
		out.ThinkingOverride = &v
	}
	out.Debug.ProjectID = strings.TrimSpace(out.Debug.ProjectID)
	return out
}

func sha256SumJSON(v any) [32]byte {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return [32]byte{}
	}
	return sha256.Sum256(raw)
}
