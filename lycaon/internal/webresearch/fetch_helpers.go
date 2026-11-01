package webresearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/textguard"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/inboundwrite"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"golang.org/x/net/html"
	"net/url"
	"path/filepath"
	"strings"
)

// fetchAssetReceipt is the JSON tool result for binary (and optional textual) writes.
type fetchAssetReceipt struct {
	OK          bool   `json:"ok"`
	Mode        string `json:"mode"`
	URL         string `json:"url"`
	Dest        string `json:"dest"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
	Status      int    `json:"status"`
	Written     bool   `json:"written"`
}

func formatAssetReceipt(res FetchRawResult, dest string, n int) (string, error) {
	sum := sha256.Sum256(res.Body)
	out := fetchAssetReceipt{
		OK:          true,
		Mode:        "raw",
		URL:         res.URL,
		Dest:        filepath.ToSlash(dest),
		Bytes:       n,
		SHA256:      hex.EncodeToString(sum[:]),
		ContentType: res.ContentType,
		Status:      res.Status,
		Written:     true,
	}
	b, err := surveyjson.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func writeFetchAsset(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	dest string,
	body []byte,
) (displayPath string, n int, err error) {
	if strings.TrimSpace(dest) == "" {
		return "", 0, &toolrejection.ToolReject{
			Code: "FETCH_URL_DEST_REQUIRED",
			Data: map[string]any{"content_type": "unknown"},
		}
	}
	receipt, err := inboundwrite.Write(ctx, boundary, tctx, dest, body, "FETCH_URL_DEST_DENIED")
	if err != nil {
		return "", 0, err
	}
	return receipt.Path, receipt.Bytes, nil
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
		return "", nil, &toolrejection.ToolReject{
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
			return "", nil, &toolrejection.ToolReject{
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

// sanitizeFetchResult strips invisible codepoints from a fetched body before it
// is cached and indexed; marking happens later at the message seam. Stub and
// empty bodies are left untouched so callers can detect extract failure.
func sanitizeFetchResult(res FetchURLResult) FetchURLResult {
	if fetchBodyUnusable(res.Text) {
		return res
	}
	// Strip only, no trimming: the cached and paged bytes stay the page's own.
	res.Text = textguard.StripInvisibleFormatRunesUntilStable(res.Text)
	return res
}

// fetchBodyUnusable reports an empty or extract-stub body — nothing to sanitize
// or cache.
func fetchBodyUnusable(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed == "" || trimmed == fetchHTMLExtractStub
}

func fetchProvenanceHost(pageURL string) string {
	u, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || u.Hostname() == "" {
		return "unknown"
	}
	return strings.ToLower(u.Hostname())
}

// isHiddenElement reports structural hiddenness from attributes only — no content
// inspection.
func isHiddenElement(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	for _, a := range n.Attr {
		key := strings.ToLower(strings.TrimSpace(a.Key))
		val := strings.ToLower(strings.TrimSpace(a.Val))
		switch key {
		case "hidden":
			return true
		case "aria-hidden":
			if val == "true" {
				return true
			}
		case "style":
			if styleHidesContent(val) {
				return true
			}
		}
	}
	return false
}

func styleHidesContent(style string) bool {
	compact := strings.ReplaceAll(style, " ", "")
	return strings.Contains(compact, "display:none") ||
		strings.Contains(compact, "visibility:hidden")
}

// expandCompactJSON re-indents a one-line JSON body so text mode can page and
// outline it by line; mode=raw keeps the bytes verbatim. Non-JSON or multi-line
// bodies are returned as is.
func expandCompactJSON(contentType, text string) (string, bool) {
	if !jsonMediaType(contentType) || strings.Count(text, "\n") > 1 {
		return text, false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return text, false
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(trimmed), "", "  "); err != nil {
		return text, false
	}
	return out.String() + "\n", true
}

func jsonMediaType(contentType string) bool {
	ct := mediaTypeOnly(contentType)
	return ct == "application/json" || strings.HasSuffix(ct, "+json")
}
