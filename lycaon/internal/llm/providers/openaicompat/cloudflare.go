package openaicompat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/pkg/api"
)

// CloudflareProvider is the chat-completions transport plus the account's
// usage quota.
type CloudflareProvider struct {
	*Provider
	accountID string
	apiKey    string
	usage     *CloudflareUsageCache
	client    *http.Client
}

func NewCloudflare(
	id, baseURL string,
	models []modelinfo.Entry,
	apiKey string,
	usage *CloudflareUsageCache,
	client *http.Client,
) modelcall.Provider {
	inner := New(id, baseURL, apiKey, models).
		WithProfile(providerprofile.CloudflareWorkersAI())
	accountID, _ := CloudflareAccountIDFromBaseURL(baseURL)
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	return &CloudflareProvider{
		Provider:  inner,
		accountID: accountID,
		apiKey:    apiKey,
		usage:     usage,
		client:    client,
	}
}

func (p *CloudflareProvider) UsageQuota(ctx context.Context) *api.ProviderUsageQuota {
	if p.usage == nil || p.accountID == "" || p.apiKey == "" {
		return nil
	}
	return p.usage.Quota(ctx, p.client, p.accountID, p.apiKey)
}

// WithHTTPRetry sets the chat retry policy, keeping the quota wrapper.
func (p *CloudflareProvider) WithHTTPRetry(policy providerretry.ProviderHTTPRetry) *CloudflareProvider {
	p.Provider.WithHTTPRetry(policy)
	return p
}

// WithPromptCache sets the provider's prompt-cache policy.
func (p *CloudflareProvider) WithPromptCache(policy providerprofile.PromptCachePolicy) *CloudflareProvider {
	p.Provider.WithPromptCache(policy)
	return p
}

// WithEffectiveModels replaces the model catalog, keeping the quota wrapper.
func (p *CloudflareProvider) WithEffectiveModels(models []modelinfo.Entry) *CloudflareProvider {
	next := *p
	next.Provider = p.Provider.WithEffectiveModels(models)
	return &next
}

const (
	// Included daily neuron allowance per account.
	cloudflareDailyNeuronAllowance = 10_000
	cloudflareNeuronUSDPer1000     = 0.011
	cloudflareAccountPlaceholder   = "YOUR_ACCOUNT_ID"
)

// CloudflareAccountIDFromBaseURL extracts the id from /accounts/{id}/ai/v1.
func CloudflareAccountIDFromBaseURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("parse base_url: %w", err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "accounts" {
			id := strings.TrimSpace(parts[i+1])
			if id == "" {
				return "", fmt.Errorf("base_url missing account id")
			}
			if strings.EqualFold(id, cloudflareAccountPlaceholder) {
				return "", fmt.Errorf("replace YOUR_ACCOUNT_ID in the endpoint URL")
			}
			return id, nil
		}
	}
	return "", fmt.Errorf("base_url missing /accounts/{account_id}/ai/ path")
}

func cloudflareUsageResetAt(now time.Time) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day()+1, 0, 0, 0, 0, time.UTC)
}

func cloudflareOverageNanoUSDFromNeurons(used float64) int64 {
	if used <= float64(cloudflareDailyNeuronAllowance) {
		return 0
	}
	usd := (used - float64(cloudflareDailyNeuronAllowance)) / 1000 * cloudflareNeuronUSDPer1000
	nano, err := cost.USDToNano(usd)
	if err != nil {
		return 0
	}
	return nano
}

const cloudflareUsageCacheTTL = 60 * time.Second

var cloudflareGraphQLURL = discovery.DefaultCloudflareAPIBaseURL + "/graphql"

type CloudflareUsageCache struct {
	mu      sync.Mutex
	entries map[string]cloudflareUsageCacheEntry
}

type cloudflareUsageCacheEntry struct {
	used    float64
	fetched time.Time
	err     error
}

func NewCloudflareUsageCache() *CloudflareUsageCache {
	return &CloudflareUsageCache{entries: make(map[string]cloudflareUsageCacheEntry)}
}

func (c *CloudflareUsageCache) Quota(
	ctx context.Context,
	client *http.Client,
	accountID, apiKey string,
) *api.ProviderUsageQuota {
	accountID = strings.TrimSpace(accountID)
	apiKey = strings.TrimSpace(apiKey)
	if accountID == "" || apiKey == "" {
		return nil
	}
	used, err := c.dailyNeurons(ctx, client, accountID, apiKey)
	if err != nil {
		return nil
	}
	now := time.Now().UTC()
	overage := cloudflareOverageNanoUSDFromNeurons(used)
	return &api.ProviderUsageQuota{
		Unit:                   "neurons",
		Used:                   used,
		Limit:                  cloudflareDailyNeuronAllowance,
		ProviderDailyAllowance: true,
		ResetsAt:               cloudflareUsageResetAt(now).Format(time.RFC3339),
		OverageNanoUSD:         &overage,
	}
}

func (c *CloudflareUsageCache) dailyNeurons(
	ctx context.Context,
	client *http.Client,
	accountID, apiKey string,
) (float64, error) {
	key := fmt.Sprintf("%s\x00%x", accountID, sha256.Sum256([]byte(apiKey)))
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && time.Since(entry.fetched) <= cloudflareUsageCacheTTL {
		used, err := entry.used, entry.err
		c.mu.Unlock()
		return used, err
	}
	c.mu.Unlock()

	used, err := fetchCloudflareDailyNeurons(ctx, client, accountID, apiKey)

	c.mu.Lock()
	c.entries[key] = cloudflareUsageCacheEntry{used: used, fetched: time.Now(), err: err}
	c.mu.Unlock()
	return used, err
}

func (c *CloudflareUsageCache) InvalidateAll() {
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}

type cloudflareGraphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type cloudflareGraphQLResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Data struct {
		Viewer struct {
			Accounts []struct {
				AIInferenceAdaptiveGroups []struct {
					Sum struct {
						TotalNeurons float64 `json:"totalNeurons"`
					} `json:"sum"`
				} `json:"aiInferenceAdaptiveGroups"`
			} `json:"accounts"`
		} `json:"viewer"`
	} `json:"data"`
}

const cloudflareDailyNeuronsQuery = `
query WorkersAIDailyNeurons($accountTag: string!, $start: Time!, $end: Time!) {
  viewer {
    accounts(filter: { accountTag: $accountTag }) {
      aiInferenceAdaptiveGroups(filter: { datetime_geq: $start, datetime_lt: $end }) {
        sum { totalNeurons }
      }
    }
  }
}`

func fetchCloudflareDailyNeurons(
	ctx context.Context,
	client *http.Client,
	accountID, apiKey string,
) (float64, error) {
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	body, err := json.Marshal(cloudflareGraphQLRequest{
		Query: cloudflareDailyNeuronsQuery,
		Variables: map[string]any{
			"accountTag": accountID,
			"start":      start.Format(time.RFC3339),
			"end":        now.Format(time.RFC3339),
		},
	})
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cloudflareGraphQLURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("cloudflare graphql HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed cloudflareGraphQLResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, err
	}
	if len(parsed.Errors) > 0 {
		return 0, fmt.Errorf("cloudflare graphql: %s", parsed.Errors[0].Message)
	}
	var total float64
	for _, acct := range parsed.Data.Viewer.Accounts {
		for _, group := range acct.AIInferenceAdaptiveGroups {
			total += group.Sum.TotalNeurons
		}
	}
	return total, nil
}
