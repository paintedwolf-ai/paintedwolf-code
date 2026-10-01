package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
)

const geminiDiscoverPageSize = 1000

// VertexExpressBaseURL is where Vertex express discovery lists models: the
// express transport cannot list them, so discovery uses the native endpoint.
const VertexExpressBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"

// geminiGenerateContentMethod marks generative rows.
const geminiGenerateContentMethod = "generateContent"

// geminiCreateCachedContentMethod distinguishes conversation models.
const geminiCreateCachedContentMethod = "createCachedContent"

type geminiNativeModelListResponse struct {
	Models        []geminiNativeModelRecord `json:"models"`
	NextPageToken string                    `json:"nextPageToken"`
}

type geminiNativeModelRecord struct {
	Name                       string   `json:"name"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
}

// GeminiModels lists conversation models from structured capabilities.
func GeminiModels(ctx context.Context, openaiCompatBaseURL, apiKey string, client *http.Client) ([]modelinfo.Entry, error) {
	nativeBase, err := geminiNativeBaseURL(openaiCompatBaseURL)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	apiKey = strings.TrimSpace(apiKey)

	var (
		out       []modelinfo.Entry
		seen      = make(map[string]struct{})
		pageToken string
	)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		page, next, err := fetchGeminiNativeModelsPage(ctx, client, nativeBase, apiKey, pageToken)
		if err != nil {
			return nil, err
		}
		for _, rec := range page {
			if !geminiSupportsChatCompletions(rec.SupportedGenerationMethods) {
				continue
			}
			id := geminiModelIDFromName(rec.Name)
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			entry := modelinfo.Entry{ID: id}
			if rec.InputTokenLimit > 0 {
				entry.ContextLength = rec.InputTokenLimit
			}
			out = append(out, entry)
		}
		if next == "" {
			break
		}
		pageToken = next
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func geminiNativeBaseURL(openaiCompatBaseURL string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(openaiCompatBaseURL), "/")
	if base == "" {
		return "", fmt.Errorf("base_url required")
	}
	trimmed := strings.TrimSuffix(base, "/openai")
	if trimmed == base {
		return "", fmt.Errorf("gemini base_url must end with /openai (got %q)", base)
	}
	if trimmed == "" {
		return "", fmt.Errorf("gemini native base_url empty")
	}
	return trimmed, nil
}

func fetchGeminiNativeModelsPage(
	ctx context.Context,
	client *http.Client,
	nativeBase, apiKey, pageToken string,
) ([]geminiNativeModelRecord, string, error) {
	u, err := url.Parse(nativeBase + "/models")
	if err != nil {
		return nil, "", err
	}
	q := u.Query()
	q.Set("pageSize", fmt.Sprintf("%d", geminiDiscoverPageSize))
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	if apiKey != "" {
		// This endpoint accepts API keys only in x-goog-api-key.
		req.Header.Set("x-goog-api-key", apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("models list HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var listed geminiNativeModelListResponse
	if err := providerhttp.DecodeDiscoveryResponse("gemini models", resp, &listed); err != nil {
		return nil, "", err
	}
	return listed.Models, strings.TrimSpace(listed.NextPageToken), nil
}

func geminiSupportsChatCompletions(methods []string) bool {
	var hasGenerate, hasCache bool
	for _, m := range methods {
		switch strings.TrimSpace(m) {
		case geminiGenerateContentMethod:
			hasGenerate = true
		case geminiCreateCachedContentMethod:
			hasCache = true
		}
	}
	return hasGenerate && hasCache
}

func geminiModelIDFromName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return strings.TrimPrefix(name, "models/")
}
