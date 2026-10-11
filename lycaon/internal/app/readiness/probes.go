package readiness

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/userpath"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/scan/bundled"
)

type Probes struct {
	service *llm.Service
	decider decide.Decider
	path    userpath.Snapshot
}

func New(service *llm.Service, decider decide.Decider, path userpath.Snapshot) *Probes {
	return &Probes{service: service, decider: decider, path: path}
}

func (b *Probes) Environment() preflight.Env {
	configDir, err := configdir.UserConfigDir()
	if err != nil {
		configDir = ""
	}

	env := preflight.DefaultEnv(
		configDir,
		b.providers,
		b.missingProviders,
		b.scanner,
		b.browser,
		b.decision,
	)
	env.LiteSlotUnavailable = b.utility
	env.UserPathSource = b.path.Source()
	env.UserPathFailure = b.path.Failure()
	return env
}

// decision reports the engine's own status: the client
// knows whether it is disabled, missing, waiting for its checkpoint, or failed
// its handshake.
func (b *Probes) decision(context.Context) preflight.DecisionReason {
	if client, ok := b.decider.(*bialy.Client); ok {
		return preflight.DecisionReason(client.Status())
	}
	// A scripted decider answers; an absent one has no engine to report on.
	if b.decider == nil || !b.decider.Available() {
		return preflight.ReasonDecisionBinaryMissing
	}
	return ""
}

func (b *Probes) utility() (bool, map[string]string) {
	if b.service == nil || b.service.Utility == nil {
		return false, nil
	}
	snap := b.service.Utility.Snapshot()
	if snap.State != llm.SlotUnavailable {
		return false, nil
	}
	detail := map[string]string{}
	if snap.Reason != "" {
		detail["reason"] = snap.Reason
	}
	if snap.ProviderID != "" {
		detail["provider_id"] = snap.ProviderID
	}
	if len(detail) == 0 {
		return true, nil
	}
	return true, detail
}

const browserPreflightTimeout = 10 * time.Second

var preflightBrowserProbe = struct {
	sync.Mutex
	identity string
}{}

// providers reads configured providers without discovery.
func (b *Probes) providers() int {
	if b.service == nil || b.service.Registry == nil {
		return 0
	}
	return b.service.Registry.ConfiguredCount()
}

func (b *Probes) missingProviders() map[string]string {
	if b.service == nil || b.service.Registry == nil || b.service.Policy == nil {
		return nil
	}
	policy, err := b.service.Policy.Get(llm.SettingsScopeGlobal, "")
	if err != nil {
		return nil
	}
	missing := map[string]string{}
	for role, ref := range map[string]llm.ModelRef{
		"coordinator": policy.Coordinator,
		"lite":        policy.Lite,
	} {
		id := strings.TrimSpace(ref.ProviderID)
		if id == "" {
			continue
		}
		if _, err := b.service.Registry.Get(id); err != nil {
			missing[role] = id
		}
	}
	for i, ref := range policy.AgentPool.Models {
		id := strings.TrimSpace(ref.ProviderID)
		if id == "" {
			continue
		}
		if _, err := b.service.Registry.Get(id); err != nil {
			missing["agent_pool."+strconv.Itoa(i)] = id
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

// browser validates the resolved browser without remediation.
func (b *Probes) browser(ctx context.Context) (preflight.BrowserReason, error) {
	cacheDir := browserengine.ManagedCacheDir()
	resolved, ok := browserengine.ResolveBinary(browserengine.ResolveOptions{CacheDir: cacheDir})
	if !ok {
		if configlayout.EngineRoot() != "" {
			return preflight.ReasonBrowserBundleMissing, errNoBrowserResolved
		}
		return preflight.ReasonBrowserManagedCacheMissing, errNoBrowserResolved
	}
	info, err := os.Stat(resolved.Path)
	if err != nil {
		return preflight.ReasonBrowserUnusable, err
	}
	identity := fmt.Sprintf("%s\x00%d\x00%d", resolved.Path, info.ModTime().UnixNano(), info.Size())
	preflightBrowserProbe.Lock()
	defer preflightBrowserProbe.Unlock()
	if preflightBrowserProbe.identity == identity {
		return "", nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, browserPreflightTimeout)
	defer cancel()
	if err := browser.ProbeUsability(probeCtx, browser.LaunchOptions{CacheDir: cacheDir}); err != nil {
		return preflight.ReasonBrowserUnusable, err
	}
	preflightBrowserProbe.identity = identity
	return "", nil
}

var errNoBrowserResolved = errors.New("no headless browser resolved (env, bundle, or managed cache)")

func (b *Probes) scanner() (string, string, error) {
	manifest, err := bundled.LoadManifest()
	if err != nil {
		return "", "not_found", err
	}

	home, err := configdir.UserConfigDir()
	if err != nil {
		home = ""
	}

	path, err := bundled.ResolveOpenGrepBinary(manifest, home, configlayout.EngineRoot())
	switch {
	case err == nil:
		return path, "", nil
	case errors.Is(err, bundled.ErrOpenGrepChecksum):
		return "", "checksum", err
	default:
		return "", "not_found", err
	}
}
