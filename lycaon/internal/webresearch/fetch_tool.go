package webresearch

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/internal/webindex"
)

func registerFetchTool(reg *tools.DefaultRegistry, deps ToolDeps) error {
	return reg.Register("fetch_url", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		rawURL, _ := args["url"].(string)
		offset := intArg(args, "offset")
		limit := intArg(args, "limit")
		// Each remote host requires its own egress decision.
		contest, _ := args["unredact"].(string)
		ctx = withToolEgressGate(ctx, deps, tctx, "fetch_url", nil, contest, screenDestination{})
		defer confine.ForgetEgressAction(tctx.Identity.SessionID, tctx.Identity.ToolCallID)
		out, fetched, err := fetchURLTool(ctx, fetchURLToolArgs{
			URL:          rawURL,
			Offset:       offset,
			Limit:        limit,
			Index:        deps.Index,
			Budget:       deps.FetchBudget,
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
			deps.FetchWarmHook(ctx, tctx.Identity.SessionID, tctx.Identity.ToolCallID, fetched.URL, fetched.Title, projectDirFrom(tctx))
		}
		return appendSecretReceipt(ctx, out), nil
	})
}

type fetchURLToolArgs struct {
	URL          string
	Offset       int
	Limit        int
	Index        *webindex.Store
	Budget       *FetchBudget
	VisualScreen *visualscreen.Gate
	Tctx         tools.ToolContext
}

// fetchURLTool returns a cached line range or a fetched structural view.
func fetchURLTool(ctx context.Context, a fetchURLToolArgs) (string, *FetchURLResult, error) {
	if a.Offset < 0 || a.Limit < 0 {
		return "", nil, &toolrejection.ToolReject{
			Code: "TOOL_ARGS_INVALID",
			Data: map[string]any{"reason": "offset and limit must be at least 1"},
		}
	}
	// A limit without an offset starts at the first line.
	if a.Limit > 0 && a.Offset == 0 {
		a.Offset = 1
	}
	if a.Offset > 0 && a.Limit == 0 {
		return "", nil, &toolrejection.ToolReject{
			Code: "TOOL_ARGS_INVALID",
			Data: map[string]any{"reason": "offset needs a limit — an open-ended range is the head-and-map default"},
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
	return fetchURLTextTool(ctx, a, rawURL)
}

func fetchURLTextTool(ctx context.Context, a fetchURLToolArgs, rawURL string) (string, *FetchURLResult, error) {
	entry, hit := tooloutput.ReadFetchCache(rawURL)
	var res FetchURLResult
	var fetched *FetchURLResult
	ext := entry.Ext
	if hit {
		res = fetchResultFromCache(entry)
		if class, reason := classifyFetchMIME(res.ContentType, rawURLPath(res.URL), []byte(res.Text)); class != mimeTextual {
			return "", nil, &toolrejection.ToolReject{Code: "FETCH_URL_TYPE_UNSUPPORTED", Data: map[string]any{"content_type": mediaTypeOnly(res.ContentType), "reason": reason}}
		}
	} else {
		release, err := acquireFetchBudget(ctx, a, rawURL)
		if err != nil {
			return "", nil, err
		}
		body, err := fetchBytes(ctx, FetchOptions{URL: rawURL})
		release()
		if err != nil {
			return "", nil, err
		}
		if isImageMIME(body.ContentType) || isImageExtension(rawURLPath(body.URL)) {
			return handleFetchVisual(ctx, a, body)
		}
		if class, reason := classifyFetchMIME(body.ContentType, rawURLPath(body.URL), body.Body); class != mimeTextual {
			return "", nil, &toolrejection.ToolReject{Code: "FETCH_URL_TYPE_UNSUPPORTED", Data: map[string]any{"content_type": mediaTypeOnly(body.ContentType), "reason": reason}}
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

func mapFetchToolErr(err error) error {
	if err == nil {
		return nil
	}
	toolReject := &toolrejection.ToolReject{}
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
		return &toolrejection.ToolReject{
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
		return &toolrejection.ToolReject{
			Code: "FETCH_URL_INVALID_URL",
			Data: map[string]any{"detail": invalidURL.Error()},
		}
	}
	var destinationDenied *egress.DestinationDeniedError
	if errors.As(err, &destinationDenied) {
		return &toolrejection.ToolReject{
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
	return b.Acquire(ctx, a.Tctx.Identity.SessionID, rawURL)
}

// noteRetrievedFrom records source attribution for live and cached results.
func noteRetrievedFrom(tctx tools.ToolContext, observedURL string) {
	if tctx.Effects.Out == nil {
		return
	}
	if host := fetchProvenanceHost(observedURL); host != "" && host != "unknown" {
		tctx.Effects.Out.RetrievedFrom = host
	}
}

func rawURLPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}
