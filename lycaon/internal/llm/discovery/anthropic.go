package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
)

type anthropicModelListResponse struct {
	Data []anthropicModelRecord `json:"data"`
}

type anthropicModelRecord struct {
	Capabilities   *anthropicModelCapabilities `json:"capabilities"`
	MaxTokens      int                         `json:"max_tokens"`
	MaxInputTokens int                         `json:"max_input_tokens"`
	ID             string                      `json:"id"`
	DisplayName    string                      `json:"display_name"`
}

// AnthropicModels lists models with the chat transport's auth and version headers.
func AnthropicModels(ctx context.Context, baseURL, apiKey string, client *http.Client) ([]modelinfo.Entry, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("base_url required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models?limit=1000", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", strings.TrimSpace(apiKey))
	req.Header.Set("anthropic-version", AnthropicVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("models list HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var listed anthropicModelListResponse
	if err := providerhttp.DecodeDiscoveryResponse("anthropic models", resp, &listed); err != nil {
		return nil, err
	}
	out := make([]modelinfo.Entry, 0, len(listed.Data))
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
		out = append(out, item.modelEntry(id))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// AnthropicVersion is the API version header sent with every request.
const AnthropicVersion = "2023-06-01"

// The Models API reports effort levels and thinking wire forms without inference.
type anthropicCapabilitySupport struct {
	Supported bool `json:"supported"`
}
type anthropicThinkingTypes struct {
	Adaptive anthropicCapabilitySupport `json:"adaptive"`
	Enabled  anthropicCapabilitySupport `json:"enabled"`
}
type anthropicThinkingCapability struct {
	Supported bool                   `json:"supported"`
	Types     anthropicThinkingTypes `json:"types"`
}
type anthropicEffortCapability struct {
	Supported bool                       `json:"supported"`
	Low       anthropicCapabilitySupport `json:"low"`
	Medium    anthropicCapabilitySupport `json:"medium"`
	High      anthropicCapabilitySupport `json:"high"`
	Xhigh     anthropicCapabilitySupport `json:"xhigh"`
	Max       anthropicCapabilitySupport `json:"max"`
}
type anthropicModelCapabilities struct {
	Thinking *anthropicThinkingCapability `json:"thinking"`
	Effort   *anthropicEffortCapability   `json:"effort"`
}

func (m anthropicModelRecord) modelEntry(id string) modelinfo.Entry {
	entry := modelinfo.Entry{ID: id, MaxTokens: m.MaxTokens, ContextLength: m.MaxInputTokens}
	if m.Capabilities == nil || m.Capabilities.Thinking == nil {
		return entry
	}
	thinking := m.Capabilities.Thinking
	c := &modelinfo.ThinkingCapabilities{State: "supported"}
	entry.DiscoveredThinking = c
	if !thinking.Supported {
		c.State = "unsupported"
		return entry
	}
	// Disabled support is not part of this API response. Only a declared model
	// rule can establish it, even when thinking itself is supported.
	if rule, ok := modelinfo.MatchThinkingRule(id); ok && rule.Controls != nil {
		c.CanDisable = rule.Controls.CanDisable && !rule.AlwaysOn
	}
	if thinking.Types.Adaptive.Supported {
		entry.DiscoveredThinkStyle = string(modelinfo.ThinkStyleAdaptive)
		c.CanEnable = true
		if effort := m.Capabilities.Effort; effort != nil && effort.Supported {
			for _, level := range []struct {
				name    string
				support anthropicCapabilitySupport
			}{
				{"low", effort.Low}, {"medium", effort.Medium}, {"high", effort.High}, {"xhigh", effort.Xhigh}, {"max", effort.Max},
			} {
				if level.support.Supported {
					c.Efforts = append(c.Efforts, level.name)
				}
			}
		}
	} else if thinking.Types.Enabled.Supported {
		entry.DiscoveredThinkStyle = string(modelinfo.ThinkStyleBudgetTokens)
		c.Budget = &modelinfo.ThinkingBudgetRange{Min: 1024}
	} else {
		c.State = "unknown"
		c.CanDisable = false
	}
	return entry
}
