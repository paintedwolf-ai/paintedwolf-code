package discovery

import (
	"bytes"
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
)

// ollamaCompletionFeature identifies chat-capable models.
const ollamaCompletionFeature = "completion"

type ollamaNativeTagsResponse struct {
	Models []ollamaNativeTagModel `json:"models"`
}

type ollamaNativeTagModel struct {
	Name string `json:"name"`
}

// OllamaModels lists chat-capable models from the native API.
func OllamaModels(ctx context.Context, baseURL, apiKey string, client *http.Client) ([]modelinfo.Entry, error) {
	nativeBase := OllamaNativeBase(baseURL)
	if nativeBase == "" {
		return nil, fmt.Errorf("base_url required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nativeBase+"/api/tags", nil)
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
		return nil, fmt.Errorf("ollama tags HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var listed ollamaNativeTagsResponse
	if err := providerhttp.DecodeDiscoveryResponse("ollama tags", resp, &listed); err != nil {
		return nil, err
	}
	out := make([]modelinfo.Entry, 0, len(listed.Models))
	seen := make(map[string]struct{}, len(listed.Models))
	for _, m := range listed.Models {
		id := strings.TrimSpace(m.Name)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		shown, showErr := discoverOllamaModel(ctx, nativeBase, id, apiKey, client)
		if showErr != nil {
			return nil, showErr
		}
		if !ollamaHasCompletionFeature(shown.Capabilities) {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, modelinfo.Entry{ID: id, ContextLength: ollamaContextLength(shown.ModelInfo), Capabilities: modelinfo.ModelCapabilities{
			Chat:      modelinfo.Evidence(modelinfo.CapabilitySupported, "ollama-show"),
			Streaming: modelinfo.Evidence(modelinfo.CapabilitySupported, "ollama-show"),
			Tools:     modelinfo.Evidence(ListAnyEvidence(shown.Capabilities, "tools"), "ollama-show"),
			Vision:    modelinfo.Evidence(ListAnyEvidence(shown.Capabilities, "vision"), "ollama-show"),
			Reasoning: modelinfo.Evidence(ListAnyEvidence(shown.Capabilities, "thinking"), "ollama-show"),
		}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func discoverOllamaModel(ctx context.Context, nativeBase, model, apiKey string, client *http.Client) (OllamaShowResponse, error) {
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return OllamaShowResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nativeBase+"/api/show", bytes.NewReader(body))
	if err != nil {
		return OllamaShowResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	resp, err := client.Do(req)
	if err != nil {
		return OllamaShowResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return OllamaShowResponse{}, fmt.Errorf("ollama show %q HTTP %d: %s", model, resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var shown OllamaShowResponse
	if err := providerhttp.DecodeDiscoveryResponse("ollama show", resp, &shown); err != nil {
		return OllamaShowResponse{}, err
	}
	return shown, nil
}

func ollamaContextLength(info map[string]json.RawMessage) int {
	contextLength := 0
	for key, raw := range info {
		if !strings.HasSuffix(strings.ToLower(key), ".context_length") {
			continue
		}
		var value int
		if json.Unmarshal(raw, &value) == nil && value > contextLength {
			contextLength = value
		}
	}
	return contextLength
}

func ollamaHasCompletionFeature(capabilities []string) bool {
	for _, c := range capabilities {
		if strings.EqualFold(strings.TrimSpace(c), ollamaCompletionFeature) {
			return true
		}
	}
	return false
}

// OllamaNativeBase converts the configured base URL to the native API root.
func OllamaNativeBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	base = strings.TrimSuffix(base, "/v1")
	return strings.TrimRight(base, "/")
}

type OllamaShowResponse struct {
	Capabilities []string                   `json:"capabilities"`
	ModelInfo    map[string]json.RawMessage `json:"model_info"`
}
