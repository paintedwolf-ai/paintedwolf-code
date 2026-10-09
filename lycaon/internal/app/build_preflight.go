package app

import (
	"context"
	"errors"
	"fmt"
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

func (b serverWiring) buildPreflightEnv() preflight.Env {
	configDir, err := configdir.UserConfigDir()
	if err != nil {
		configDir = ""
	}

	env := preflight.DefaultEnv(
		configDir,
		b.providerCountForPreflight,
		b.missingRoleProvidersForPreflight,
		b.resolveScannerForPreflight,
		b.checkBrowserForPreflight,
		b.checkDecisionEngineForPreflight,
	)
	env.LiteSlotUnavailable = b.liteSlotUnavailableForPreflight
	env.UserPathSource = b.processes.Path.Source()
	env.UserPathFailure = b.processes.Path.Failure()
	return env
}

// checkDecisionEngineForPreflight reports the engine's own status: the client
// knows whether it is disabled, missing, waiting for its checkpoint, or failed
// its handshake.
func (b serverWiring) checkDecisionEngineForPreflight(context.Context) preflight.DecisionReason {
	if client, ok := b.decisions.Decider.(*bialy.Client); ok {
		return preflight.DecisionReason(client.Status())
	}
	// A scripted decider answers; an absent one has no engine to report on.
	if b.decisions.Decider == nil || !b.decisions.Decider.Available() {
		return preflight.ReasonDecisionBinaryMissing
	}
	return ""
}

func (b serverWiring) liteSlotUnavailableForPreflight() (bool, map[string]string) {
	if b.providers.Service == nil || b.providers.Service.Utility == nil {
		return false, nil
	}
	snap := b.providers.Service.Utility.Snapshot()
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

// providerCountForPreflight reads configured providers without discovery.
func (b serverWiring) providerCountForPreflight() int {
	if b.providers.Service == nil || b.providers.Service.Registry == nil {
		return 0
	}
	return b.providers.Service.Registry.ConfiguredCount()
}

func (b serverWiring) missingRoleProvidersForPreflight() map[string]string {
	if b.providers.Service == nil || b.providers.Service.Registry == nil || b.providers.Service.Policy == nil {
		return nil
	}
	policy, err := b.providers.Service.Policy.Get(llm.SettingsScopeGlobal, "")
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
		if _, err := b.providers.Service.Registry.Get(id); err != nil {
			missing[role] = id
		}
	}
	for i, ref := range policy.AgentPool.Models {
		id := strings.TrimSpace(ref.ProviderID)
		if id == "" {
			continue
		}
		if _, err := b.providers.Service.Registry.Get(id); err != nil {
			missing["agent_pool."+strconv.Itoa(i)] = id
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

// checkBrowserForPreflight validates the resolved browser without remediation.
func (b serverWiring) checkBrowserForPreflight(ctx context.Context) (preflight.BrowserReason, error) {
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

func (b serverWiring) resolveScannerForPreflight() (string, string, error) {
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
