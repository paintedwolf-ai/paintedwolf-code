// Package discovery reads provider model catalogs and interprets their structured metadata.
// It does not select providers, publish registry snapshots, or issue completions.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/pricing"
)

// Profile configures composable model listing.
type Profile struct {
	// ListPath is appended to base_url (default "/models").
	ListPath string
	// Query is the raw query string without a leading ?.
	Query string
	// ResponseShape selects the list JSON parser.
	ResponseShape ResponseShape
	// IncludeWireTypes keeps rows whose wire "type" field matches (case-insensitive).
	// Empty means no type filter.
	IncludeWireTypes []string
	// RequireOutputModality keeps rows that include this output token.
	RequireOutputModality string
	// RequirePositiveTokenPrice excludes provider rows with no positive input/output price.
	RequirePositiveTokenPrice bool
	// PricingScale normalizes wire prices to per-1k USD.
	PricingScale PricingScale
}

// ResponseShape selects how a model list response is decoded.
type ResponseShape string

const (
	DiscoveryShapeOpenAIData ResponseShape = "openai_data"
	// DiscoveryShapeCatalogArray parses a top-level catalog array.
	DiscoveryShapeCatalogArray ResponseShape = "catalog_array"
	// DiscoveryShapeOpenRouter parses rich model records.
	DiscoveryShapeOpenRouter ResponseShape = "openrouter"
)

// PricingScale converts discovered price fields to catalog per-1k USD.
type PricingScale string

const (
	// DiscoveryPricingPerMillion treats wire prices as USD per 1M tokens.
	DiscoveryPricingPerMillion PricingScale = "per_million"
	// DiscoveryPricingPerToken treats wire prices as USD per token.
	DiscoveryPricingPerToken PricingScale = "per_token"
)

type discoveryCatalogRecord struct {
	Thinking            *openRouterThinking
	ID                  string
	WireType            string
	ContextLength       int
	InputPrice          *float64
	OutputPrice         *float64
	CacheReadPrice      *float64
	CacheReadPriceRaw   string
	CacheWritePriceRaw  string
	InputPriceRaw       string
	OutputPriceRaw      string
	OutputModalities    []string
	InputModalities     []string
	SupportedParameters []string
}

func OpenAIProfile() Profile {
	return Profile{
		ListPath:      "/models",
		ResponseShape: DiscoveryShapeOpenAIData,
	}
}

// ProfileUntyped reports whether discovery lacks a typed chat filter.
func ProfileUntyped(profile Profile) bool {
	profile = normalizeDiscoveryProfile(profile)
	if profile.ResponseShape != "" && profile.ResponseShape != DiscoveryShapeOpenAIData {
		return false
	}
	if len(profile.IncludeWireTypes) > 0 {
		return false
	}
	if strings.TrimSpace(profile.RequireOutputModality) != "" {
		return false
	}
	if profile.RequirePositiveTokenPrice {
		return false
	}
	return true
}

func TogetherProfile() Profile {
	return Profile{
		ListPath:                  "/models",
		ResponseShape:             DiscoveryShapeCatalogArray,
		IncludeWireTypes:          []string{"chat"},
		RequirePositiveTokenPrice: true,
		PricingScale:              DiscoveryPricingPerMillion,
	}
}

func OpenRouterProfile() Profile {
	return Profile{
		ListPath:              "/models",
		Query:                 "output_modalities=text",
		ResponseShape:         DiscoveryShapeOpenRouter,
		RequireOutputModality: "text",
		PricingScale:          DiscoveryPricingPerToken,
	}
}

// FromProfile lists models using the composable discovery profile.
func FromProfile(
	ctx context.Context,
	baseURL, apiKey string,
	client *http.Client,
	profile Profile,
) ([]modelinfo.Entry, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("base_url required")
	}
	if client == nil {
		client = providerhttp.DiscoveryClient(httpclient.DiscoveryTimeout)
	}
	profile = normalizeDiscoveryProfile(profile)

	listURL := base + profile.ListPath
	if profile.Query != "" {
		listURL += "?" + profile.Query
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
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

	records, err := decodeDiscoveryCatalogRecords(resp.Body, profile.ResponseShape)
	if err != nil {
		return nil, err
	}
	out := make([]modelinfo.Entry, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, rec := range records {
		if !discoveryRecordAllowed(rec, profile) {
			continue
		}
		id := strings.TrimSpace(rec.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		entry := modelinfo.Entry{ID: id, Untyped: ProfileUntyped(profile)}
		if !entry.Untyped {
			entry.Capabilities.Chat = modelinfo.Evidence(modelinfo.CapabilitySupported, "provider-discovery")
		}
		entry.Callable = discoveryCallability(rec, profile)
		if profile.ResponseShape == DiscoveryShapeOpenRouter {
			entry.DiscoveredThinking = rec.Thinking.capabilities(id)
			entry.Capabilities.Tools = modelinfo.Evidence(ListAnyEvidence(rec.SupportedParameters, "tools"), "openrouter")
			entry.Capabilities.Reasoning = modelinfo.Evidence(ListAnyEvidence(rec.SupportedParameters, "reasoning"), "openrouter")
			entry.Capabilities.StructuredOutput = modelinfo.Evidence(
				ListAnyEvidence(rec.SupportedParameters, "structured_outputs", "response_format"), "openrouter",
			)
		}
		if rec.ContextLength > 0 {
			entry.ContextLength = rec.ContextLength
		}
		applyDiscoveryPricing(&entry, rec, profile.PricingScale)
		applyDiscoveryVision(&entry, rec)
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func normalizeDiscoveryProfile(profile Profile) Profile {
	if strings.TrimSpace(profile.ListPath) == "" {
		profile.ListPath = "/models"
	}
	return profile
}

func decodeDiscoveryCatalogRecords(r io.Reader, shape ResponseShape) ([]discoveryCatalogRecord, error) {
	switch shape {
	case DiscoveryShapeCatalogArray:
		return decodeDiscoveryCatalogArray(r)
	case DiscoveryShapeOpenRouter:
		return decodeDiscoveryOpenRouterRecords(r)
	default:
		return decodeDiscoveryOpenAIDataRecords(r)
	}
}

func decodeDiscoveryOpenAIDataRecords(r io.Reader) ([]discoveryCatalogRecord, error) {
	var listed struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&listed); err != nil {
		return nil, err
	}
	out := make([]discoveryCatalogRecord, 0, len(listed.Data))
	for _, item := range listed.Data {
		out = append(out, discoveryCatalogRecord{
			ID:            strings.TrimSpace(item.ID),
			ContextLength: item.ContextLength,
		})
	}
	return out, nil
}

type discoveryArrayModel struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Input       *float64 `json:"input"`
		Output      *float64 `json:"output"`
		CachedInput *float64 `json:"cached_input"`
	} `json:"pricing"`
}

func decodeDiscoveryCatalogArray(r io.Reader) ([]discoveryCatalogRecord, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}
	var listed []discoveryArrayModel
	if err := json.Unmarshal(raw, &listed); err != nil {
		var wrapped struct {
			Data []discoveryArrayModel `json:"data"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, err
		}
		listed = wrapped.Data
	}
	out := make([]discoveryCatalogRecord, 0, len(listed))
	for _, item := range listed {
		for _, price := range []*float64{item.Pricing.Input, item.Pricing.Output, item.Pricing.CachedInput} {
			if price != nil && (*price < 0 || math.IsNaN(*price) || math.IsInf(*price, 0)) {
				return nil, fmt.Errorf("invalid discovered price for model %q", item.ID)
			}
		}
		out = append(out, discoveryCatalogRecord{
			ID: strings.TrimSpace(item.ID), WireType: strings.TrimSpace(item.Type),
			ContextLength: item.ContextLength, InputPrice: item.Pricing.Input,
			OutputPrice: item.Pricing.Output, CacheReadPrice: item.Pricing.CachedInput,
		})
	}
	return out, nil
}

func decodeDiscoveryOpenRouterRecords(r io.Reader) ([]discoveryCatalogRecord, error) {
	var listed struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			Pricing       struct {
				Prompt     string `json:"prompt"`
				CacheRead  string `json:"input_cache_read"`
				CacheWrite string `json:"input_cache_write"`
				Completion string `json:"completion"`
			} `json:"pricing"`
			Architecture struct {
				InputModalities  []string `json:"input_modalities"`
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
			SupportedParameters []string            `json:"supported_parameters"`
			Thinking            *openRouterThinking `json:"reasoning"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&listed); err != nil {
		return nil, err
	}
	out := make([]discoveryCatalogRecord, 0, len(listed.Data))
	for _, item := range listed.Data {
		out = append(out, discoveryCatalogRecord{
			Thinking:            item.Thinking,
			ID:                  strings.TrimSpace(item.ID),
			ContextLength:       item.ContextLength,
			InputPriceRaw:       strings.TrimSpace(item.Pricing.Prompt),
			CacheReadPriceRaw:   strings.TrimSpace(item.Pricing.CacheRead),
			CacheWritePriceRaw:  strings.TrimSpace(item.Pricing.CacheWrite),
			OutputPriceRaw:      strings.TrimSpace(item.Pricing.Completion),
			OutputModalities:    append([]string(nil), item.Architecture.OutputModalities...),
			InputModalities:     append([]string(nil), item.Architecture.InputModalities...),
			SupportedParameters: append([]string(nil), item.SupportedParameters...),
		})
	}
	return out, nil
}

func discoveryRecordAllowed(rec discoveryCatalogRecord, profile Profile) bool {
	if len(profile.IncludeWireTypes) > 0 && !discoveryWireTypeAllowed(rec.WireType, profile.IncludeWireTypes) {
		return false
	}
	if profile.RequireOutputModality != "" {
		if len(rec.OutputModalities) == 0 {
			return false
		}
		found := false
		for _, mod := range rec.OutputModalities {
			if strings.EqualFold(strings.TrimSpace(mod), strings.TrimSpace(profile.RequireOutputModality)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// discoveryCallability derives callability from host-declared pricing.
func discoveryCallability(rec discoveryCatalogRecord, profile Profile) modelinfo.CapabilityEvidence {
	if !profile.RequirePositiveTokenPrice {
		return modelinfo.CapabilityEvidence{}
	}
	if (rec.InputPrice != nil && *rec.InputPrice > 0) || (rec.OutputPrice != nil && *rec.OutputPrice > 0) {
		return modelinfo.Evidence(modelinfo.CapabilitySupported, CallabilitySource)
	}
	return modelinfo.Evidence(modelinfo.CapabilityUnsupported, CallabilitySource)
}

const CallabilitySource = "provider-listing"

func discoveryWireTypeAllowed(wireType string, allowed []string) bool {
	wireType = strings.ToLower(strings.TrimSpace(wireType))
	for _, want := range allowed {
		if wireType == strings.ToLower(strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func applyDiscoveryPricing(entry *modelinfo.Entry, rec discoveryCatalogRecord, scale PricingScale) {
	rate := pricing.Rate{Currency: "USD"}
	switch scale {
	case DiscoveryPricingPerMillion:
		rate.InputPer1K = pricing.Scale(rec.InputPrice, 0.001)
		rate.OutputPer1K = pricing.Scale(rec.OutputPrice, 0.001)
		rate.CacheReadPer1K = pricing.Scale(rec.CacheReadPrice, 0.001)
	case DiscoveryPricingPerToken:
		if v, ok := discoveryPricePer1KFromToken(rec.InputPriceRaw); ok {
			rate.InputPer1K = &v
		}
		if v, ok := discoveryPricePer1KFromToken(rec.OutputPriceRaw); ok {
			rate.OutputPer1K = &v
		}
		if v, ok := discoveryPricePer1KFromToken(rec.CacheReadPriceRaw); ok {
			rate.CacheReadPer1K = &v
		}
		if v, ok := discoveryPricePer1KFromToken(rec.CacheWritePriceRaw); ok {
			rate.CacheWritePer1K = &v
		}
	}
	modelinfo.ApplyDiscoveredRate(entry, rate)
}

func discoveryPricePer1KFromToken(pricePerToken string) (float64, bool) {
	pricePerToken = strings.TrimSpace(pricePerToken)
	if pricePerToken == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(pricePerToken, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v * 1000, true
}

func applyDiscoveryVision(entry *modelinfo.Entry, rec discoveryCatalogRecord) {
	if modelinfo.Supported(entry.Capabilities.Vision) {
		return
	}
	state := ListAnyEvidence(rec.InputModalities, "image")
	if state != modelinfo.CapabilityUnknown {
		entry.Capabilities.Vision = modelinfo.Evidence(state, "provider-discovery")
	}
}

func ListAnyEvidence(values []string, wanted ...string) modelinfo.CapabilityState {
	if values == nil {
		return modelinfo.CapabilityUnknown
	}
	for _, value := range values {
		for _, want := range wanted {
			if strings.EqualFold(strings.TrimSpace(value), want) {
				return modelinfo.CapabilitySupported
			}
		}
	}
	return modelinfo.CapabilityUnsupported
}
