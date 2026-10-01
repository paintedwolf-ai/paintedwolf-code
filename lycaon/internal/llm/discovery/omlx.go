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

// omlxConversationModelTypes identifies conversation-capable rows.
var omlxConversationModelTypes = map[string]struct{}{
	"llm": {},
	"vlm": {},
}

type omlxModelsStatusResponse struct {
	Models []omlxModelStatusRecord `json:"models"`
}

type omlxModelStatusRecord struct {
	ID         string `json:"id"`
	ModelAlias string `json:"model_alias"`
	ModelType  string `json:"model_type"`
}

// OMLXModels lists conversation models from typed status rows.
func OMLXModels(ctx context.Context, baseURL, apiKey string, client *http.Client) ([]modelinfo.Entry, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("base_url required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models/status", nil)
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
		return nil, fmt.Errorf("omlx models status HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var listed omlxModelsStatusResponse
	if err := providerhttp.DecodeDiscoveryResponse("omlx models status", resp, &listed); err != nil {
		return nil, err
	}
	out := make([]modelinfo.Entry, 0, len(listed.Models))
	seen := make(map[string]struct{}, len(listed.Models))
	for _, item := range listed.Models {
		if _, ok := omlxConversationModelTypes[strings.ToLower(strings.TrimSpace(item.ModelType))]; !ok {
			continue
		}
		id := strings.TrimSpace(item.ModelAlias)
		if id == "" {
			id = strings.TrimSpace(item.ID)
		}
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, modelinfo.Entry{ID: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
