package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
)

const defaultFireworksDiscoveryBaseURL = "https://api.fireworks.ai"
const fireworksDiscoveryPageSize = 200

// fireworksDiscoveryOrigin keeps discovery on the configured origin.
func fireworksDiscoveryOrigin(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return defaultFireworksDiscoveryBaseURL
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return defaultFireworksDiscoveryBaseURL
	}
	return u.Scheme + "://" + u.Host
}

type fireworksListModelsResponse struct {
	Models        []fireworksModel `json:"models"`
	NextPageToken string           `json:"nextPageToken"`
}

// fireworksEmbeddingModelKind identifies embedding-only rows.
const fireworksEmbeddingModelKind = "EMBEDDING_MODEL"

type fireworksModel struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// ConversationConfig is present for chat-capable rows.
	ConversationConfig json.RawMessage `json:"conversationConfig"`
}

// isChatCapable checks the catalog's structured capability fields.
func (m fireworksModel) isChatCapable() bool {
	if strings.EqualFold(strings.TrimSpace(m.Kind), fireworksEmbeddingModelKind) {
		return false
	}
	cc := strings.TrimSpace(string(m.ConversationConfig))
	return cc != "" && cc != "null"
}

// FireworksModels lists callable chat models.
func FireworksModels(ctx context.Context, baseURL, apiKey string, client *http.Client) ([]string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("fireworks api key required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}

	origin := fireworksDiscoveryOrigin(baseURL)
	var (
		ids       []string
		pageToken string
	)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		page, next, err := fetchFireworksServerlessPage(ctx, client, origin, apiKey, pageToken)
		if err != nil {
			return nil, err
		}
		ids = append(ids, page...)
		if next == "" {
			break
		}
		pageToken = next
	}
	sort.Strings(ids)
	return ids, nil
}

func fetchFireworksServerlessPage(
	ctx context.Context,
	client *http.Client,
	origin, apiKey, pageToken string,
) ([]string, string, error) {
	u, err := url.Parse(origin + "/v1/accounts/fireworks/models")
	if err != nil {
		return nil, "", err
	}
	q := u.Query()
	q.Set("filter", "supports_serverless=true")
	q.Set("pageSize", fmt.Sprintf("%d", fireworksDiscoveryPageSize))
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("fireworks models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var listed fireworksListModelsResponse
	if err := providerhttp.DecodeDiscoveryResponse("fireworks models", resp, &listed); err != nil {
		return nil, "", err
	}
	ids := make([]string, 0, len(listed.Models))
	for _, m := range listed.Models {
		if !m.isChatCapable() {
			continue
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		ids = append(ids, name)
	}
	return ids, strings.TrimSpace(listed.NextPageToken), nil
}

// FireworksTimeout bounds native catalog pagination.
const FireworksTimeout = 8 * time.Second
