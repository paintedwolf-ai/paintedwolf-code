package webresearch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/internal/webindex"
	"github.com/lycaon/lycaon/pkg/api"
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
	// Boundary is required for mode=raw dest writes (same sandbox as write/copy).
	Boundary *sandbox.Boundary
	// FetchBudget soft-caps outbound fetch_url network fetches; nil → GlobalFetchBudget().
	FetchBudget *FetchBudget
	// SecretMatcher screens outbound content; nil or Inert() ⇒ no ask.
	SecretMatcher *secretmatch.Matcher
	// SecretAsk raises the outbound-secret tool_approval ask; nil ⇒ deny on match.
	SecretAsk secretmatch.AskFunc
	// VisualStore persists visual artifacts; nil disables visual ingestion.
	VisualStore visual.Store
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
			return "", &tools.ToolReject{
				Code:        webSearchPeriodInvalidCode,
				Observation: webSearchPeriodInvalidObservation,
				Data:        map[string]any{"period": strings.TrimSpace(rawPeriod), "detail": err.Error(), "period_min_year": periodFloorYear, "period_max_year": periodCeilingYear},
			}
		}
		settings := settingsFn()
		if !settings.SearchEnabled {
			return "", &tools.ToolReject{
				Code:        webSearchDisabledCode,
				Observation: webSearchDisabledObservation,
			}
		}
		contest, _ := args["unredact"].(string)
		ctx = withToolEgressGate(ctx, deps, tctx, "web_search", DeclaredEndpointHosts(settings, deps.Registry), contest,
			webSearchDestination(settings, deps.Registry))
		defer confine.ForgetEgressAction(tctx.SessionID, tctx.ToolCallID)
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
			SessionID:  tctx.SessionID,
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
			return "", &tools.ToolReject{
				Code:        webSearchProvidersFailedCode,
				Observation: webSearchProvidersFailedObservation,
				Data:        map[string]any{"detail": result.Error, "search_provider_statuses": statuses},
			}
		}
		if !result.OK {
			return "", &tools.ToolReject{
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
			deps.SearchWarmHook(ctx, tctx.SessionID, tctx.ToolCallID, strings.TrimSpace(query), projectDirFrom(tctx), hitURLs, result.residualURLs, strongHits, maxR, directPart)
		}
		out, err := surveyjson.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		if tctx.Out != nil {
			// The query identifies a search result spanning multiple hosts.
			tctx.Out.RetrievedFrom = strings.Join(strings.Fields(query), " ")
		}
		return appendPeriodHint(appendSecretReceipt(ctx, string(out)), query, period), nil
	}); err != nil {
		return err
	}

	if err := reg.Register("fetch_url", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		rawURL, _ := args["url"].(string)
		mode, _ := args["mode"].(string)
		dest, _ := args["dest"].(string)
		offset := intArg(args, "offset")
		limit := intArg(args, "limit")
		// Model-supplied destinations retain per-host approval checks.
		contest, _ := args["unredact"].(string)
		ctx = withToolEgressGate(ctx, deps, tctx, "fetch_url", nil, contest, screenDestination{})
		defer confine.ForgetEgressAction(tctx.SessionID, tctx.ToolCallID)
		out, fetched, err := fetchURLTool(ctx, fetchURLToolArgs{
			URL:          rawURL,
			Mode:         mode,
			Dest:         dest,
			Offset:       offset,
			Limit:        limit,
			Index:        deps.Index,
			Boundary:     deps.Boundary,
			Budget:       deps.FetchBudget,
			VisualStore:  deps.VisualStore,
			VisualScreen: deps.VisualScreen,
			Tctx:         tctx,
		})
		if err != nil {
			return "", mapFetchToolErr(err)
		}
		if host, denied := egressgate.Denied(ctx); denied {
			return "", hostDeniedReject(host)
		}
		if fault, ok := SecretScreenFaulted(ctx); ok {
			return "", secretScreenFaultReject(fault)
		}
		if denied, ok := SecretScreenDenied(ctx); ok {
			return "", secretScreenRejectFromErr(denied)
		}
		if deps.FetchWarmHook != nil && fetched != nil {
			deps.FetchWarmHook(ctx, tctx.SessionID, tctx.ToolCallID, fetched.URL, fetched.Title, projectDirFrom(tctx))
		}
		return appendSecretReceipt(ctx, out), nil
	}); err != nil {
		return err
	}
	return nil
}

// withToolEgressGate attaches declared destinations and secret fanout.
func withToolEgressGate(ctx context.Context, deps ToolDeps, tctx tools.ToolContext, image string, declaredHosts []string, contestToken string, fanout screenDestination) context.Context {
	attr := secretmatch.AskAttribution{
		SessionID:     tctx.SessionID,
		RootSessionID: tctx.ChatSessionID(),
		ProjectID:     tctx.ProjectID,
		ProjectDir:    tctx.ActiveRootPath(),
		ToolCallID:    tctx.ToolCallID,
	}
	ctx = secretmatch.WithAskAttribution(ctx, attr)
	ctx = egressgate.WithAttribution(ctx, confine.EgressCommand{
		SessionID:     tctx.SessionID,
		RootSessionID: tctx.ChatSessionID(),
		ProjectID:     tctx.ProjectID,
		ProjectDir:    tctx.ActiveRootPath(),
		ToolCallID:    tctx.ToolCallID,
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
func WireRuntime() (Runtime, error) {
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
	if err := RegisterCatalogProviders(reg); err != nil {
		return Runtime{}, err
	}
	limits, err := LoadFetchURLLimits()
	if err != nil {
		return Runtime{}, fmt.Errorf("fetch_url limits: %w", err)
	}
	SetGlobalFetchBudget(NewFetchBudget(limits))
	return Runtime{Creds: creds, Config: cfg, Catalog: cat, Registry: reg}, nil
}

type fetchURLToolArgs struct {
	URL          string
	Mode         string
	Dest         string
	Offset       int
	Limit        int
	Index        *webindex.Store
	Boundary     *sandbox.Boundary
	Budget       *FetchBudget
	VisualStore  visual.Store
	VisualScreen *visualscreen.Gate
	Tctx         tools.ToolContext
}

// fetchURLTool returns a cached line range or a fetched structural view.
func fetchURLTool(ctx context.Context, a fetchURLToolArgs) (string, *FetchURLResult, error) {
	if a.Offset < 0 || a.Limit < 0 {
		return "", nil, &tools.ToolReject{
			Code: "TOOL_ARGS_INVALID",
			Data: map[string]any{"reason": "offset and limit must be at least 1"},
		}
	}
	// A limit without an offset starts at the first line.
	if a.Limit > 0 && a.Offset == 0 {
		a.Offset = 1
	}
	if a.Offset > 0 && a.Limit == 0 {
		return "", nil, &tools.ToolReject{
			Code: "TOOL_ARGS_INVALID",
			Data: map[string]any{"reason": "offset needs a limit — an open-ended range is the head-and-map default"},
		}
	}
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	if mode == "" {
		mode = "text"
	}
	if mode != "text" && mode != "raw" {
		return "", nil, &tools.ToolReject{
			Code: "FETCH_URL_MODE_INVALID",
			Data: map[string]any{"mode": a.Mode},
		}
	}
	dest := strings.TrimSpace(a.Dest)
	if dest != "" && mode != "raw" {
		return "", nil, &tools.ToolReject{
			Code: "FETCH_URL_DEST_INVALID",
			Data: map[string]any{"dest": dest},
		}
	}

	rawURL := CanonicalFetchURL(a.URL)
	u, err := normalizeFetchURL(rawURL)
	if err != nil {
		return "", nil, err
	}
	// Secret screening uses the invocation context before cache access or fetching.
	screenedURL, err := screenOutbound(ctx, secretmatch.SurfaceFetchURL, httpDestination(u), u.String())
	if err != nil {
		return "", nil, err
	}
	if screenedURL != u.String() {
		u, err = normalizeFetchURL(screenedURL)
		if err != nil {
			return "", nil, err
		}
		rawURL = u.String()
	}
	if mode == "text" && isImageExtension(u.Path) {
		mode = "raw"
	}
	if mode == "raw" {
		return fetchURLRawTool(ctx, a, rawURL, dest)
	}
	return fetchURLTextTool(ctx, a, rawURL)
}

func fetchURLTextTool(ctx context.Context, a fetchURLToolArgs, rawURL string) (string, *FetchURLResult, error) {
	entry, hit := tooloutput.ReadFetchCache(rawURL)
	var res FetchURLResult
	var fetched *FetchURLResult
	ext := entry.Ext
	if hit {
		res = fetchResultFromCache(entry)
	} else {
		release, err := acquireFetchBudget(ctx, a, rawURL)
		if err != nil {
			return "", nil, err
		}
		body, err := fetchBytes(ctx, FetchOptions{URL: rawURL}, "text")
		release()
		if err != nil {
			return "", nil, err
		}
		if isImageMIME(body.ContentType) {
			return handleFetchVisual(ctx, a, body)
		}
		res = fetchTextResult(body)
		fetched = &res
		ext = fetchCacheExt(res)
		// Redirecting URLs retain the caller's cache key.
		_ = tooloutput.WriteFetchCache(rawURL, fetchResultCacheEntry(res, ext))
		ingestFetchedPage(ctx, a.Index, res)
	}
	if a.Offset > 0 && a.Limit > 0 {
		noteRetrievedFrom(a.Tctx, res.URL)
		return FormatFetchRange(res.URL, res.Text, a.Offset, a.Limit), fetched, nil
	}
	var outline fileoutline.Result
	if len(res.Text) > inlineFullMaxBytes {
		outline = fileoutline.AnalyzeText(ctx, "page."+ext, []byte(res.Text))
	}
	noteRetrievedFrom(a.Tctx, res.URL)
	return FormatFetchResult(res, outline, hit), fetched, nil
}

func fetchURLRawTool(ctx context.Context, a fetchURLToolArgs, rawURL, dest string) (string, *FetchURLResult, error) {
	// Raw text can reuse cached bytes; binary assets are fetched directly.
	if dest == "" {
		if entry, hit := tooloutput.ReadFetchCache("raw:" + rawURL); hit {
			res := fetchResultFromCache(entry)
			if a.Offset > 0 && a.Limit > 0 {
				noteRetrievedFrom(a.Tctx, res.URL)
				return FormatFetchRange(res.URL, res.Text, a.Offset, a.Limit), nil, nil
			}
			var outline fileoutline.Result
			if len(res.Text) > inlineFullMaxBytes {
				outline = fileoutline.AnalyzeText(ctx, "page."+entry.Ext, []byte(res.Text))
			}
			noteRetrievedFrom(a.Tctx, res.URL)
			return FormatFetchResult(res, outline, true), nil, nil
		}
	}

	release, err := acquireFetchBudget(ctx, a, rawURL)
	if err != nil {
		return "", nil, err
	}
	live, err := FetchRaw(ctx, FetchOptions{URL: rawURL})
	release()
	if err != nil {
		return "", nil, err
	}

	urlPath := ""
	if u, err := url.Parse(live.URL); err == nil {
		urlPath = u.Path
	}
	class, reason := classifyFetchMIME(live.ContentType, urlPath, live.Body)
	switch class {
	case mimeUnsupported:
		return "", nil, &tools.ToolReject{
			Code: "FETCH_URL_TYPE_UNSUPPORTED",
			Data: map[string]any{
				"content_type": mediaTypeOnly(live.ContentType),
				"reason":       reason,
			},
		}
	case mimeBinary:
		if dest == "" {
			if isImageMIME(live.ContentType) || isImageExtension(urlPath) {
				return handleFetchVisual(ctx, a, live)
			}
			return "", nil, &tools.ToolReject{
				Code: "FETCH_URL_DEST_REQUIRED",
				Data: map[string]any{"content_type": mediaTypeOnly(live.ContentType)},
			}
		}
		written, n, werr := writeFetchAsset(ctx, a.Boundary, a.Tctx, dest, live.Body)
		if werr != nil {
			return "", nil, werr
		}
		out, err := formatAssetReceipt(live, written, n)
		return out, nil, err
	default:
		if (isImageMIME(live.ContentType) || strings.HasSuffix(strings.ToLower(urlPath), ".svg")) && dest == "" {
			return handleFetchVisual(ctx, a, live)
		}
		text := string(live.Body)
		res := FetchURLResult{
			URL:         live.URL,
			Status:      live.Status,
			ContentType: live.ContentType,
			Text:        text,
			Markdown:    false,
		}
		if dest != "" {
			written, n, werr := writeFetchAsset(ctx, a.Boundary, a.Tctx, dest, live.Body)
			if werr != nil {
				return "", nil, werr
			}
			receipt, err := formatAssetReceipt(live, written, n)
			if err != nil {
				return "", nil, err
			}
			// Prefetch still needs the body pageable — cache under raw: key and
			// append a short write notice above the text view.
			_ = tooloutput.WriteFetchCache("raw:"+rawURL, rawResultCacheEntry(live, text))
			if a.Offset > 0 && a.Limit > 0 {
				noteRetrievedFrom(a.Tctx, res.URL)
				return receipt + "\n\n" + FormatFetchRange(res.URL, res.Text, a.Offset, a.Limit), &res, nil
			}
			var outline fileoutline.Result
			if len(res.Text) > inlineFullMaxBytes {
				outline = fileoutline.AnalyzeText(ctx, "page."+fetchRawCacheExt(live), []byte(res.Text))
			}
			noteRetrievedFrom(a.Tctx, res.URL)
			return receipt + "\n\n" + FormatFetchResult(res, outline, false), &res, nil
		}
		_ = tooloutput.WriteFetchCache("raw:"+rawURL, rawResultCacheEntry(live, text))
		ingestFetchedPage(ctx, a.Index, res)
		if a.Offset > 0 && a.Limit > 0 {
			return FormatFetchRange(res.URL, res.Text, a.Offset, a.Limit), &res, nil
		}
		var outline fileoutline.Result
		if len(res.Text) > inlineFullMaxBytes {
			outline = fileoutline.AnalyzeText(ctx, "page."+fetchRawCacheExt(live), []byte(res.Text))
		}
		noteRetrievedFrom(a.Tctx, res.URL)
		return FormatFetchResult(res, outline, false), &res, nil
	}
}

func isImageMIME(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return strings.HasPrefix(ct, "image/") || ct == "image/svg+xml"
}

func isImageExtension(urlPath string) bool {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(urlPath)))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico":
		return true
	default:
		return false
	}
}

// handleFetchVisual screens a fetched image before perception. The receipt
// names the image by the artifact id the host stamps on the result; the
// evidence ledger mints its citation handle.
func handleFetchVisual(ctx context.Context, a fetchURLToolArgs, live FetchRawResult) (string, *FetchURLResult, error) {
	ct := mediaTypeOnly(live.ContentType)
	if ct == "" {
		if strings.HasSuffix(strings.ToLower(live.URL), ".svg") {
			ct = "image/svg+xml"
		} else {
			ct = "image/png"
		}
	}
	rejectData := map[string]any{"url": live.URL, "path": live.URL, "format": strings.TrimPrefix(ct, "image/")}
	perceiveBytes, perceiveMime := live.Body, ct
	receipt := map[string]any{
		"url":          live.URL,
		"content_type": ct,
		"size_bytes":   len(live.Body),
		"mode":         "visual",
	}
	perceive := true
	if a.VisualScreen != nil {
		outcome, err := a.VisualScreen.Screen(ctx, visualscreen.VisualScreenInput{
			SessionID:     a.Tctx.SessionID,
			ProjectID:     a.Tctx.ProjectID,
			ToolName:      "fetch_url",
			ToolCallID:    a.Tctx.ToolCallID,
			Mime:          ct,
			RawBytes:      live.Body,
			PublicInbound: true,
		})
		if err != nil {
			if errors.Is(err, visualscreen.ErrVisualSecretWithheld) {
				return "", nil, &tools.ToolReject{
					Code: "FETCH_URL_SECRET_WITHHELD",
					Data: map[string]any{"url": live.URL},
				}
			}
			return "", nil, fetchImageReject(err, rejectData)
		}
		if outcome.ExtractedText != "" {
			receipt["text"] = runeclamp.Clamp(outcome.ExtractedText, 4000)
		}
		if outcome.Perception != visualscreen.PerceptionOriginal {
			receipt["perception"] = string(outcome.Perception)
		}
		if outcome.Gap != "" {
			receipt["screening_gap"] = string(outcome.Gap)
		}
		perceiveBytes, perceiveMime, perceive = outcome.PerceiveBytes, outcome.Mime, outcome.PermitPerception()
	} else if kind, err := visualscreen.Classify(ct, live.Body); err != nil {
		return "", nil, fetchImageReject(err, rejectData)
	} else if kind == visualscreen.KindRaster {
		if _, err := visualscreen.CheckRasterBounds(live.Body); err != nil {
			return "", nil, fetchImageReject(err, rejectData)
		}
	}

	if perceive {
		if a.Tctx.Out == nil {
			a.Tctx.Out = &tools.ToolInvocationOut{}
		}
		a.Tctx.Out.Visual = &tools.VisualCapture{
			Mime:      perceiveMime,
			Bytes:     append([]byte(nil), perceiveBytes...),
			Source:    api.VisualArtifactSourceFetch,
			Caption:   live.URL,
			Perceive:  true,
			Projected: true,
		}
	}
	raw, _ := surveyjson.Marshal(receipt)
	noteRetrievedFrom(a.Tctx, live.URL)
	return string(raw), nil, nil
}

func fetchImageReject(err error, data map[string]any) error {
	var dims *visualscreen.DimensionsError
	if errors.As(err, &dims) {
		out := map[string]any{"width": dims.Width, "height": dims.Height, "max_dimension": dims.Max}
		for k, v := range data {
			out[k] = v
		}
		return &tools.ToolReject{Code: "IMAGE_DIMENSIONS_EXCEEDED", Data: out}
	}
	if errors.Is(err, visualscreen.ErrImageUndecodable) {
		out := map[string]any{"rejection_reason": err.Error()}
		for k, v := range data {
			out[k] = v
		}
		return &tools.ToolReject{Code: "IMAGE_CORRUPTED", Data: out}
	}
	return err
}

func fetchResultCacheEntry(res FetchURLResult, ext string) tooloutput.FetchCacheEntry {
	return tooloutput.FetchCacheEntry{
		URL:         res.URL,
		Status:      res.Status,
		ContentType: res.ContentType,
		Title:       res.Title,
		Body:        res.Text,
		Ext:         ext,
		Markdown:    res.Markdown,
	}
}

func rawResultCacheEntry(res FetchRawResult, body string) tooloutput.FetchCacheEntry {
	return tooloutput.FetchCacheEntry{
		URL:         res.URL,
		Status:      res.Status,
		ContentType: res.ContentType,
		Body:        body,
		Ext:         fetchRawCacheExt(res),
	}
}

func fetchResultFromCache(entry tooloutput.FetchCacheEntry) FetchURLResult {
	return FetchURLResult{
		URL:         entry.URL,
		Status:      entry.Status,
		ContentType: entry.ContentType,
		Title:       entry.Title,
		Text:        entry.Body,
		Markdown:    entry.Markdown,
	}
}

func fetchCacheExt(res FetchURLResult) string {
	if res.Markdown {
		return "md"
	}
	// The declared media type selects the outline grammar for extensionless URLs.
	if jsonMediaType(res.ContentType) {
		return "json"
	}
	if u, err := url.Parse(res.URL); err == nil {
		if e := path.Ext(u.Path); len(e) > 1 {
			return e[1:]
		}
	}
	return "txt"
}

func fetchRawCacheExt(res FetchRawResult) string {
	if u, err := url.Parse(res.URL); err == nil {
		if e := path.Ext(u.Path); len(e) > 1 {
			return e[1:]
		}
	}
	ct := mediaTypeOnly(res.ContentType)
	switch {
	case strings.Contains(ct, "javascript"):
		return "js"
	case strings.Contains(ct, "json"):
		return "json"
	case strings.Contains(ct, "css"):
		return "css"
	case strings.Contains(ct, "svg"):
		return "svg"
	case strings.Contains(ct, "html"):
		return "html"
	case strings.Contains(ct, "xml"):
		return "xml"
	}
	return "txt"
}

func mapFetchToolErr(err error) error {
	if err == nil {
		return nil
	}
	toolReject := &tools.ToolReject{}
	if errors.As(err, &toolReject) {
		return err
	}
	var denied *egressgate.HostDeniedError
	if errors.As(err, &denied) {
		return hostDeniedReject(denied.Host)
	}
	var secretFault *SecretScreenFaultError
	if errors.As(err, &secretFault) {
		return secretScreenFaultReject(secretFault)
	}
	var secretDenied *SecretDeniedError
	if errors.As(err, &secretDenied) {
		return secretScreenRejectFromErr(secretDenied)
	}
	var tooLarge FetchBodyTooLargeError
	if errors.As(err, &tooLarge) {
		return &tools.ToolReject{
			Code: "FETCH_URL_BODY_TOO_LARGE",
			Data: map[string]any{
				"bytes":          tooLarge.Actual,
				"max_bytes":      tooLarge.Limit,
				"resource_limit": true,
			},
		}
	}
	var invalidURL *InvalidURLError
	if errors.As(err, &invalidURL) {
		return &tools.ToolReject{
			Code: "FETCH_URL_INVALID_URL",
			Data: map[string]any{"detail": invalidURL.Error()},
		}
	}
	var destinationDenied *egress.DestinationDeniedError
	if errors.As(err, &destinationDenied) {
		return &tools.ToolReject{
			Code: "FETCH_URL_BLOCKED",
			Data: map[string]any{"detail": destinationDenied.Error()},
		}
	}
	return err
}

func acquireFetchBudget(ctx context.Context, a fetchURLToolArgs, rawURL string) (func(), error) {
	b := a.Budget
	if b == nil {
		b = GlobalFetchBudget()
	}
	return b.Acquire(ctx, a.Tctx.SessionID, rawURL)
}

func projectDirFrom(tctx tools.ToolContext) string {
	if root, err := projectroot.ActiveRoot(tctx.Roots, tctx.ActiveRootID); err == nil {
		return root.Path
	}
	if root, err := projectroot.PrimaryRoot(tctx.Roots); err == nil {
		return root.Path
	}
	return ""
}

// noteRetrievedFrom records source attribution for live and cached results.
func noteRetrievedFrom(tctx tools.ToolContext, observedURL string) {
	if tctx.Out == nil {
		return
	}
	if host := fetchProvenanceHost(observedURL); host != "" && host != "unknown" {
		tctx.Out.RetrievedFrom = host
	}
}
