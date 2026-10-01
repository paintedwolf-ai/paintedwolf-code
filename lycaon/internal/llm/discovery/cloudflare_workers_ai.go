package discovery

import (
	"context"
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

const (
	DefaultCloudflareAPIBaseURL   = "https://api.cloudflare.com/client/v4"
	cloudflareDiscoverPageSize    = 100
	CloudflareTimeout             = 8 * time.Second
	cloudflareDiscoverTaskTextGen = "text-generation"
)

var CloudflareAPIBaseURL = DefaultCloudflareAPIBaseURL

type cloudflareModelsSearchResponse struct {
	Success    bool                    `json:"success"`
	Result     []cloudflareModelRecord `json:"result"`
	ResultInfo cloudflareResultInfo    `json:"result_info"`
}

type cloudflareResultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalPages int `json:"total_pages"`
}

type cloudflareModelRecord struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// CloudflareModels lists text-generation models from its catalog API.
func CloudflareModels(
	ctx context.Context,
	accountID, apiKey string,
	client *http.Client,
) ([]string, error) {
	accountID = strings.TrimSpace(accountID)
	apiKey = strings.TrimSpace(apiKey)
	if accountID == "" {
		return nil, fmt.Errorf("cloudflare account id required")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("cloudflare api token required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}

	seen := make(map[string]struct{})
	var ids []string
	for page := 1; ; page++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		pageIDs, totalPages, err := fetchCloudflareModelsPage(ctx, client, accountID, apiKey, page)
		if err != nil {
			return nil, err
		}
		for _, id := range pageIDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		if totalPages == 0 || page >= totalPages {
			break
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func fetchCloudflareModelsPage(
	ctx context.Context,
	client *http.Client,
	accountID, apiKey string,
	page int,
) ([]string, int, error) {
	u, err := url.Parse(CloudflareAPIBaseURL + "/accounts/" + url.PathEscape(accountID) + "/ai/models/search")
	if err != nil {
		return nil, 0, err
	}
	q := u.Query()
	q.Set("task", cloudflareDiscoverTaskTextGen)
	q.Set("page", fmt.Sprintf("%d", page))
	q.Set("per_page", fmt.Sprintf("%d", cloudflareDiscoverPageSize))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, 0, fmt.Errorf("cloudflare models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var listed cloudflareModelsSearchResponse
	if err := providerhttp.DecodeDiscoveryResponse("cloudflare models", resp, &listed); err != nil {
		return nil, 0, err
	}
	if !listed.Success {
		return nil, 0, fmt.Errorf("cloudflare models search failed")
	}

	ids := make([]string, 0, len(listed.Result))
	for _, item := range listed.Result {
		id := strings.TrimSpace(item.Name)
		if id == "" {
			id = strings.TrimSpace(item.ID)
		}
		if id == "" || !strings.HasPrefix(id, "@cf/") {
			continue
		}
		ids = append(ids, id)
	}
	return ids, listed.ResultInfo.TotalPages, nil
}
