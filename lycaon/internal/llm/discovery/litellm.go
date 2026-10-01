package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/pricing"
)

type litellmModelInfoResponse struct {
	Data []litellmModelInfoRecord `json:"data"`
}

type litellmModelInfoRecord struct {
	ModelName string           `json:"model_name"`
	ModelInfo litellmModelInfo `json:"model_info"`
}

type litellmModelInfo struct {
	Rate                    pricing.Rate `json:"-"`
	Mode                    string       `json:"mode"`
	MaxInputTokens          int          `json:"max_input_tokens"`
	SupportsVision          *bool        `json:"supports_vision"`
	SupportsFunctionCalling *bool        `json:"supports_function_calling"`
	SupportsReasoning       *bool        `json:"supports_reasoning"`
	SupportsResponseSchema  *bool        `json:"supports_response_schema"`
	SupportsPromptCaching   *bool        `json:"supports_prompt_caching"`
}

func (info *litellmModelInfo) UnmarshalJSON(raw []byte) error {
	type plain litellmModelInfo
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*info = litellmModelInfo(decoded)
	// Invalid prices leave model discovery available.
	if rate, err := pricing.ParseLiteLLMRate(raw); err == nil && rate.Valid() {
		info.Rate = rate
	}
	return nil
}

// LiteLLMModels lists chat models with structured capabilities.
func LiteLLMModels(ctx context.Context, baseURL, apiKey string, client *http.Client) ([]modelinfo.Entry, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("base_url required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	records, status, err := fetchLiteLLMModelInfo(ctx, client, base, apiKey)
	if err == nil {
		return litellmChatCapableEntries(records), nil
	}
	if status != http.StatusNotFound {
		return nil, err
	}
	ids, fallbackErr := OpenAIModels(ctx, base, apiKey, client)
	if fallbackErr != nil {
		return nil, fallbackErr
	}
	return modelinfo.UntypedEntriesFromIDs(ids), nil
}

func fetchLiteLLMModelInfo(ctx context.Context, client *http.Client, base, apiKey string) ([]litellmModelInfoRecord, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/model/info", nil)
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, resp.StatusCode, fmt.Errorf("litellm model info HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var listed litellmModelInfoResponse
	if err := providerhttp.DecodeDiscoveryResponse("litellm model info", resp, &listed); err != nil {
		return nil, resp.StatusCode, err
	}
	return listed.Data, resp.StatusCode, nil
}

func litellmChatCapableEntries(records []litellmModelInfoRecord) []modelinfo.Entry {
	out := make([]modelinfo.Entry, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, rec := range records {
		mode := strings.ToLower(strings.TrimSpace(rec.ModelInfo.Mode))
		if mode != "" && mode != "chat" {
			continue
		}
		id := strings.TrimSpace(rec.ModelName)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		entry := modelinfo.Entry{ID: id, Capabilities: modelinfo.ModelCapabilities{
			Chat:             modelinfo.Evidence(modelinfo.CapabilitySupported, "litellm"),
			Streaming:        modelinfo.Evidence(modelinfo.CapabilitySupported, "litellm"),
			Tools:            modelinfo.Evidence(modelinfo.OptionalState(rec.ModelInfo.SupportsFunctionCalling), "litellm"),
			Reasoning:        modelinfo.Evidence(modelinfo.OptionalState(rec.ModelInfo.SupportsReasoning), "litellm"),
			StructuredOutput: modelinfo.Evidence(modelinfo.OptionalState(rec.ModelInfo.SupportsResponseSchema), "litellm"),
			PromptCaching:    modelinfo.Evidence(modelinfo.OptionalState(rec.ModelInfo.SupportsPromptCaching), "litellm"),
		}}
		if rec.ModelInfo.MaxInputTokens > 0 {
			entry.ContextLength = rec.ModelInfo.MaxInputTokens
		}
		entry.Capabilities.Vision = modelinfo.Evidence(modelinfo.OptionalState(rec.ModelInfo.SupportsVision), "litellm")
		if rec.ModelInfo.SupportsVision != nil && *rec.ModelInfo.SupportsVision {
			entry.Capabilities.Vision = modelinfo.Evidence(modelinfo.CapabilitySupported, "litellm")
		}
		modelinfo.ApplyDiscoveredRate(&entry, rec.ModelInfo.Rate)
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
