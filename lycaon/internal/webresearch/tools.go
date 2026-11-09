package webresearch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/internal/webindex"
)

// SearchWarmHook starts attributed index expansion after a search.
type SearchWarmHook func(
	ctx context.Context,
	sessionID, toolCallID, query, projectDir string,
	hitURLs, residualURLs []string,
	strongHits, maxResults int,
	directParticipated bool,
)

// FetchWarmHook expands a freshly fetched host using the page title as its topic.
// Cache hits do not trigger it.
type FetchWarmHook func(ctx context.Context, sessionID, toolCallID, pageURL, title, projectDir string)

// ToolDeps wires catalog-backed search into native tools.
type ToolDeps struct {
	Creds    *CredentialStore
	Config   *ConfigStore
	Catalog  *Catalog
	Registry *Registry
	// Index is the persistent web index; nil disables ingestion and memory.
	Index *webindex.Store
	// Rerank blends the decision engine into verified page order.
	Rerank decide.Reranker
	// SearchWarmHook runs after web_search returns hits; nil disables post-search warming.
	SearchWarmHook SearchWarmHook
	// FetchWarmHook runs after fetch_url fetches a fresh page; nil disables post-fetch warming.
	FetchWarmHook FetchWarmHook
	// FetchBudget soft-caps outbound fetch_url network fetches; nil → GlobalFetchBudget().
	FetchBudget *FetchBudget
	// SecretMatcher screens outbound content; nil or Inert() ⇒ no ask.
	SecretMatcher *secretmatch.Matcher
	// SecretAsk raises the outbound-secret tool_approval ask; nil ⇒ deny on match.
	SecretAsk secretmatch.AskFunc
	// VisualScreen coordinates pre-perception credential screening.
	VisualScreen *visualscreen.Gate
}

const (
	webSearchDisabledCode               = "WEB_SEARCH_DISABLED"
	webSearchDisabledObservation        = "search_disabled"
	webSearchProvidersFailedCode        = "WEB_SEARCH_PROVIDERS_FAILED"
	webSearchProvidersFailedObservation = "web_search_providers_failed"
	webSearchPeriodInvalidCode          = "WEB_SEARCH_PERIOD_INVALID"
	webSearchPeriodInvalidObservation   = "period_invalid"
)

// RegisterToolsWithFactory registers web_search and fetch_url with an
// invocation-scoped discoverer factory.
func RegisterToolsWithFactory(reg *tools.DefaultRegistry, deps ToolDeps, getFactory FactoryGetter) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	settingsFn := func() Settings {
		return DefaultSettings(deps.Creds, deps.Config, deps.Catalog)
	}
	if err := reg.Register("web_search", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		query, _ := args["query"].(string)
		limit := intArg(args, "limit")
		rawPeriod, _ := args["period"].(string)
		period, err := ParsePeriod(rawPeriod)
		if err != nil {
			return "", &toolrejection.ToolReject{
				Code:        webSearchPeriodInvalidCode,
				Observation: webSearchPeriodInvalidObservation,
				Data:        map[string]any{"period": strings.TrimSpace(rawPeriod), "detail": err.Error(), "period_min_year": periodFloorYear, "period_max_year": periodCeilingYear},
			}
		}
		settings := settingsFn()
		if !settings.SearchEnabled {
			return "", &toolrejection.ToolReject{
				Code:        webSearchDisabledCode,
				Observation: webSearchDisabledObservation,
			}
		}
		contest, _ := args["unredact"].(string)
		ctx = withToolEgressGate(ctx, deps, tctx, "web_search", DeclaredEndpointHosts(settings, deps.Registry), contest,
			webSearchDestination(settings, deps.Registry))
		defer confine.ForgetEgressAction(tctx.Identity.SessionID, tctx.Identity.ToolCallID)
		var discoverer DirectDiscoverer
		if getFactory != nil {
			if factory := getFactory(); factory != nil {
				discoverer = factory(ctx, tctx)
			}
		}
		// The host owns provider selection and parallel scheduling.
		rerankLedger := decide.NewRerankLedger()
		result := Search(ctx, SearchOptions{
			Query:      query,
			Period:     period,
			Limit:      limit,
			Settings:   settings,
			Discoverer: discoverer,
			Registry:   deps.Registry,
			Index:      deps.Index,
			Rerank:     deps.Rerank.WithLedger(rerankLedger),
			SessionID:  tctx.Identity.SessionID,
		})
		result.Rerank = rerankLedger.Receipt()
		if host, denied := egressgate.Denied(ctx); denied {
			return "", hostDeniedReject(host)
		}
		if fault, ok := SecretScreenFaulted(ctx); ok {
			return "", secretScreenFaultReject(fault)
		}
		if denied, ok := SecretScreenDenied(ctx); ok {
			return "", secretScreenRejectFromErr(denied)
		}
		if !result.OK && len(result.ProvidersSkipped) > 0 {
			statuses := make([]string, 0, len(result.ProvidersSkipped))
			for _, skipped := range result.ProvidersSkipped {
				statuses = append(statuses, skipped.Provider+": "+skipped.Reason)
			}
			return "", &toolrejection.ToolReject{
				Code:        webSearchProvidersFailedCode,
				Observation: webSearchProvidersFailedObservation,
				Data:        map[string]any{"detail": result.Error, "search_provider_statuses": statuses},
			}
		}
		if !result.OK {
			return "", &toolrejection.ToolReject{
				Code: "TOOL_ARGS_INVALID",
				Data: map[string]any{"reason": result.Error},
			}
		}
		hitURLs := make([]string, 0, len(result.Results))
		for _, h := range result.Results {
			if u := strings.TrimSpace(h.URL); u != "" {
				hitURLs = append(hitURLs, u)
			}
		}
		strongHits := result.directStrongHits
		maxR := result.directMaxResults
		directPart := result.directParticipated
		underfilled := directPart && maxR > 0 && strongHits*2 < maxR
		shouldWarm := result.OK && (len(hitURLs) > 0 || len(result.residualURLs) > 0 || underfilled)
		if deps.SearchWarmHook != nil && shouldWarm {
			deps.SearchWarmHook(ctx, tctx.Identity.SessionID, tctx.Identity.ToolCallID, strings.TrimSpace(query), projectDirFrom(tctx), hitURLs, result.residualURLs, strongHits, maxR, directPart)
		}
		out, err := surveyjson.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		if tctx.Effects.Out != nil {
			// The query identifies a search result spanning multiple hosts.
			tctx.Effects.Out.RetrievedFrom = strings.Join(strings.Fields(query), " ")
		}
		return appendPeriodHint(appendSecretReceipt(ctx, string(out)), query, period), nil
	}); err != nil {
		return err
	}

	return registerFetchTool(reg, deps)
}

// withToolEgressGate attaches declared destinations and secret fanout.
func withToolEgressGate(ctx context.Context, deps ToolDeps, tctx tools.ToolContext, image string, declaredHosts []string, contestToken string, fanout screenDestination) context.Context {
	attr := secretmatch.AskAttribution{
		SessionID:     tctx.Identity.SessionID,
		RootSessionID: tctx.ChatSessionID(),
		ProjectID:     tctx.Identity.ProjectID,
		ProjectDir:    tctx.ActiveRootPath(),
		ToolCallID:    tctx.Identity.ToolCallID,
	}
	ctx = secretmatch.WithAskAttribution(ctx, attr)
	ctx = egressgate.WithAttribution(ctx, confine.EgressCommand{
		SessionID:     tctx.Identity.SessionID,
		RootSessionID: tctx.ChatSessionID(),
		ProjectID:     tctx.Identity.ProjectID,
		ProjectDir:    tctx.ActiveRootPath(),
		ToolCallID:    tctx.Identity.ToolCallID,
		Image:         image,
		DeclaredHosts: declaredHosts,
	})
	return withSecretScreen(ctx, deps.SecretMatcher, deps.SecretAsk, contestToken, fanout)
}

// The enricher renders redaction receipts through the hint registry.
func appendSecretReceipt(ctx context.Context, out string) string {
	token, count, ok := SecretScreenReceipt(ctx)
	if !ok {
		return out
	}
	return out + guidance.FormatSecretReceiptMarker(token, count)
}

// appendPeriodHint suggests the structured period argument when applicable.
func appendPeriodHint(out, query string, period Period) string {
	if !period.IsCurrent() {
		return out
	}
	year := bareYearToken(query, time.Now())
	if year == "" {
		return out
	}
	return out + guidance.FormatPeriodHintMarker(year)
}

func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case int32:
		return int(v)
	}
	return 0
}

// WireRuntime loads the catalog before catalog-dependent stores.
func WireRuntime(ctx context.Context) (Runtime, error) {
	cat, err := LoadCatalog()
	if err != nil {
		return Runtime{}, err
	}
	creds, err := NewCredentialStore(cat)
	if err != nil {
		return Runtime{}, fmt.Errorf("web research credentials: %w", err)
	}
	cfg, err := NewConfigStore()
	if err != nil {
		return Runtime{}, err
	}
	reg := NewRegistry(cat)
	if err := RegisterCatalogProviders(ctx, reg); err != nil {
		return Runtime{}, err
	}
	limits, err := LoadFetchURLLimits()
	if err != nil {
		return Runtime{}, fmt.Errorf("fetch_url limits: %w", err)
	}
	SetGlobalFetchBudget(NewFetchBudget(limits))
	return Runtime{Creds: creds, Config: cfg, Catalog: cat, Registry: reg}, nil
}

func projectDirFrom(tctx tools.ToolContext) string {
	if root, err := projectroot.ActiveRoot(tctx.Source.Roots, tctx.Source.ActiveRootID); err == nil {
		return root.Path
	}
	if root, err := projectroot.PrimaryRoot(tctx.Source.Roots); err == nil {
		return root.Path
	}
	return ""
}
