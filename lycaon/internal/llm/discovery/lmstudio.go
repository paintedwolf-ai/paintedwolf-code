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

// lmstudioChatTypes identifies conversation-capable rows.
var lmstudioChatTypes = map[string]struct{}{
	"llm": {},
	"vlm": {},
}

type lmstudioModelListResponse struct {
	Data []lmstudioModelRecord `json:"data"`
}

type lmstudioModelRecord struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Capabilities struct {
		Vision            *bool `json:"vision"`
		TrainedForToolUse *bool `json:"trained_for_tool_use"`
	} `json:"capabilities"`
}

// LMStudioModels lists conversation models from typed catalog rows.
func LMStudioModels(ctx context.Context, baseURL, apiKey string, client *http.Client) ([]modelinfo.Entry, error) {
	nativeBase := lmstudioNativeBase(baseURL)
	if nativeBase == "" {
		return nil, fmt.Errorf("base_url required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nativeBase+"/api/v0/models", nil)
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
		return nil, fmt.Errorf("lmstudio models HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var listed lmstudioModelListResponse
	if err := providerhttp.DecodeDiscoveryResponse("lmstudio models", resp, &listed); err != nil {
		return nil, err
	}
	out := make([]modelinfo.Entry, 0, len(listed.Data))
	seen := make(map[string]struct{}, len(listed.Data))
	for _, item := range listed.Data {
		typ := strings.ToLower(strings.TrimSpace(item.Type))
		if _, ok := lmstudioChatTypes[typ]; !ok {
			continue
		}
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		entry := modelinfo.Entry{ID: id, Capabilities: modelinfo.ModelCapabilities{
			Chat:      modelinfo.Evidence(modelinfo.CapabilitySupported, "lmstudio"),
			Streaming: modelinfo.Evidence(modelinfo.CapabilitySupported, "lmstudio"),
			Tools:     modelinfo.Evidence(modelinfo.OptionalState(item.Capabilities.TrainedForToolUse), "lmstudio"),
		}}
		entry.Capabilities.Vision = modelinfo.Evidence(modelinfo.OptionalState(item.Capabilities.Vision), "lmstudio")
		if typ == "vlm" || item.Capabilities.Vision != nil && *item.Capabilities.Vision {
			entry.Capabilities.Vision = modelinfo.Evidence(modelinfo.CapabilitySupported, "lmstudio")
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// lmstudioNativeBase resolves the native API root.
func lmstudioNativeBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	base = strings.TrimSuffix(base, "/v1")
	return strings.TrimRight(base, "/")
}
