package llm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
)

func (r *Registry) discoverModelsFresh(ctx context.Context, providerID string, profile providerprofile.Profile, baseURL, apiKey string) ([]modelinfo.Entry, error) {
	key := discoveryCacheKey(providerID, profile, baseURL, apiKey)
	return r.discovery.Load(ctx, key, func(ctx context.Context) ([]modelinfo.Entry, error) {
		return fetchDiscoveredModels(ctx, r.discoveryClient, profile, baseURL, apiKey)
	})
}

func fetchDiscoveredModels(ctx context.Context, client *http.Client, profile providerprofile.Profile, baseURL, apiKey string) ([]modelinfo.Entry, error) {
	if profile.Discovery == providerprofile.DiscoveryNone {
		return nil, nil
	}
	var discovered []modelinfo.Entry
	var err error
	switch profile.Discovery {
	case providerprofile.DiscoveryFireworks:
		fwCtx, fwCancel := context.WithTimeout(ctx, discovery.FireworksTimeout)
		var ids []string
		ids, err = discovery.FireworksModels(fwCtx, baseURL, apiKey, client)
		fwCancel()
		discovered = modelinfo.EntriesFromIDs(ids)
	case providerprofile.DiscoveryCloudflareWorkersAI:
		accountID, acctErr := openaicompat.CloudflareAccountIDFromBaseURL(baseURL)
		if acctErr != nil {
			err = acctErr
			break
		}
		cfCtx, cfCancel := context.WithTimeout(ctx, discovery.CloudflareTimeout)
		var ids []string
		ids, err = discovery.CloudflareModels(cfCtx, accountID, apiKey, client)
		cfCancel()
		discovered = modelinfo.EntriesFromIDs(ids)
	case providerprofile.DiscoveryGemini:
		probeCtx, cancel := context.WithTimeout(ctx, discoverModelsTimeout)
		discovered, err = discovery.GeminiModels(probeCtx, baseURL, apiKey, client)
		cancel()
	case providerprofile.DiscoveryVertexExpress:
		probeCtx, cancel := context.WithTimeout(ctx, discoverModelsTimeout)
		discovered, err = discovery.GeminiModels(probeCtx, discovery.VertexExpressBaseURL, apiKey, client)
		cancel()
	case providerprofile.DiscoveryAnthropic:
		probeCtx, cancel := context.WithTimeout(ctx, discoverModelsTimeout)
		discovered, err = discovery.AnthropicModels(probeCtx, baseURL, apiKey, client)
		cancel()
	case providerprofile.DiscoveryAzure:
		probeCtx, cancel := context.WithTimeout(ctx, discoverModelsTimeout)
		discovered, err = discovery.AzureModels(probeCtx, baseURL, client)
		cancel()
	case providerprofile.DiscoveryOllama:
		probeCtx, cancel := context.WithTimeout(ctx, discoverModelsTimeout)
		discovered, err = discovery.OllamaModels(probeCtx, baseURL, apiKey, client)
		cancel()
	case providerprofile.DiscoveryLMStudio:
		probeCtx, cancel := context.WithTimeout(ctx, discoverModelsTimeout)
		discovered, err = discovery.LMStudioModels(probeCtx, baseURL, apiKey, client)
		cancel()
	case providerprofile.DiscoveryOMLX:
		probeCtx, cancel := context.WithTimeout(ctx, discoverModelsTimeout)
		discovered, err = discovery.OMLXModels(probeCtx, baseURL, apiKey, client)
		cancel()
	case providerprofile.DiscoveryLiteLLM:
		probeCtx, cancel := context.WithTimeout(ctx, discoverModelsTimeout)
		discovered, err = discovery.LiteLLMModels(probeCtx, baseURL, apiKey, client)
		cancel()
	case providerprofile.DiscoveryBedrock:
		brCtx, brCancel := context.WithTimeout(ctx, discovery.BedrockTimeout)
		discovered, err = discovery.BedrockModels(brCtx, baseURL, apiKey)
		brCancel()
	case providerprofile.DiscoveryVertex:
		vxCtx, vxCancel := context.WithTimeout(ctx, discovery.VertexTimeout)
		discovered, err = discovery.VertexModels(vxCtx, client)
		vxCancel()
	default:
		probeCtx, cancel := context.WithTimeout(ctx, discoverModelsTimeout)
		discovered, err = discovery.FromProfile(probeCtx, baseURL, apiKey, client, profile.DiscoveryProfile)
		cancel()
	}
	if err != nil {
		return nil, err
	}
	return discovered, nil
}

func discoveryCacheKey(providerID string, profile providerprofile.Profile, baseURL, apiKey string) string {
	credentialDigest := sha256.Sum256([]byte(apiKey))
	return fmt.Sprintf("%s\x00%s\x00%s\x00%x", providerID, profile.Discovery, baseURL, credentialDigest)
}
