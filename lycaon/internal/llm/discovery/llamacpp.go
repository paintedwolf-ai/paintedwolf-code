package discovery

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
)

// LlamaCppSource identifies context windows read from llama.cpp GET /props.
const LlamaCppSource = "llamacpp-props"

type llamaCppPropsResponse struct {
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
	NCtx       int    `json:"n_ctx"`
	ModelAlias string `json:"model_alias"`
	ModelPath  string `json:"model_path"`
}

// LlamaCppNativeBase resolves the server root by stripping any trailing /v1.
func LlamaCppNativeBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	base = strings.TrimSuffix(base, "/v1")
	return strings.TrimRight(base, "/")
}

// ProbeLlamaCppProps queries GET /props on a llama.cpp-compatible server and
// returns the slot serving context window (default_generation_settings.n_ctx or
// top-level n_ctx), or 0 when unavailable.
func ProbeLlamaCppProps(ctx context.Context, baseURL, apiKey string, client *http.Client) int {
	nCtx, _ := probeLlamaCppPropsDetail(ctx, baseURL, apiKey, client)
	return nCtx
}

func probeLlamaCppPropsDetail(ctx context.Context, baseURL, apiKey string, client *http.Client) (nCtx int, modelID string) {
	nativeBase := LlamaCppNativeBase(baseURL)
	if nativeBase == "" {
		return 0, ""
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nativeBase+"/props", nil)
	if err != nil {
		return 0, ""
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, ""
	}
	var props llamaCppPropsResponse
	if err := providerhttp.DecodeDiscoveryResponse("llamacpp props", resp, &props); err != nil {
		return 0, ""
	}
	nCtx = props.DefaultGenerationSettings.NCtx
	if nCtx <= 0 {
		nCtx = props.NCtx
	}
	if nCtx <= 0 {
		return 0, ""
	}
	modelID = strings.TrimSpace(props.ModelAlias)
	if modelID == "" && strings.TrimSpace(props.ModelPath) != "" {
		modelID = filepath.Base(strings.TrimSpace(props.ModelPath))
	}
	return nCtx, modelID
}
