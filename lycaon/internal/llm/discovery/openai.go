package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
)

type openAIModelListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// OpenAIModels lists model ids from a compatible GET /models endpoint.
func OpenAIModels(ctx context.Context, baseURL, apiKey string, client *http.Client) ([]string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("base_url required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("models list HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var listed openAIModelListResponse
	if err := providerhttp.DecodeDiscoveryResponse("openai-compatible models", resp, &listed); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(listed.Data))
	seen := make(map[string]struct{}, len(listed.Data))
	for _, item := range listed.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
