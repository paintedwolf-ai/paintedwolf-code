package llm

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/modelfeed"
)

// DiscoveryStatus values for ProviderMeta.discovery_status.
const (
	DiscoveryStatusOK      = "ok"
	DiscoveryStatusEmpty   = "empty"
	DiscoveryStatusError   = "error"
	DiscoveryStatusSkipped = "skipped"
)

// mergeResult is the visible assignable model list plus status bits.
type mergeResult struct {
	Models               []modelinfo.Entry
	CatalogAuthoritative bool
	CatalogStatus        string
	DiscoveryStatus      string
	DiscoveryError       error
	// Refused retains host-declined rows for assignment errors.
	Refused []modelinfo.Entry
}

// mergeAssignableModels builds the assignable list for one provider instance.
//
// Catalog-authoritative: ConversationEligible feed rows form the base; local
// models[] overlay matching ids only; empty discovery must not wipe the base.
// Typed discovery may append live ids; untyped discovery only intersects the
// eligible catalog (no embed/dall-e leaks).
//
// Discovery-authoritative: typed discovery list (empty → empty). Untyped
// id-only lists are empty unless local models[] allowlists ids that also
// appear live.
func mergeAssignableModels(
	kind string,
	local []modelinfo.Entry,
	discovered []modelinfo.Entry,
	discoverErr error,
	doc *modelfeed.Document,
	catalogStatus string,
	feedUsable bool,
) mergeResult {
	// A device limit constrains resolved capacity; it is not capacity evidence.
	metadata := modelinfo.CloneEntries(local)
	for i := range metadata {
		metadata[i].ContextLength = 0
	}
	out := mergeCatalogSources(kind, metadata, discovered, discoverErr, doc, catalogStatus, feedUsable)
	applyModelContextCaps(kind, out.Models, local)
	out.Models, out.Refused = partitionRefusedModels(out.Models)
	return out
}

func applyModelContextCaps(kind string, models, local []modelinfo.Entry) {
	for i := range models {
		configured, ok := catalogPricingForID(kind, local, models[i].ID)
		if !ok || configured.ContextLength <= 0 {
			continue
		}
		if models[i].ContextLength <= 0 || configured.ContextLength < models[i].ContextLength {
			models[i].ContextLength = configured.ContextLength
		}
	}
}

// partitionRefusedModels separates explicit host refusals.
func partitionRefusedModels(models []modelinfo.Entry) (assignable, refused []modelinfo.Entry) {
	for _, model := range models {
		if modelinfo.Refused(model.Callable) {
			refused = append(refused, model)
			continue
		}
		assignable = append(assignable, model)
	}
	return assignable, refused
}

// mergeCatalogSources applies feed, local, and discovery authority.
func mergeCatalogSources(
	kind string,
	local []modelinfo.Entry,
	discovered []modelinfo.Entry,
	discoverErr error,
	doc *modelfeed.Document,
	catalogStatus string,
	feedUsable bool,
) mergeResult {
	authoritative := CatalogAuthoritative(kind, feedUsable)
	if catalogStatus == "" {
		if feedUsable {
			catalogStatus = modelfeed.StatusOK
		} else {
			catalogStatus = modelfeed.StatusUnavailable
		}
	}

	out := mergeResult{
		CatalogAuthoritative: authoritative,
		CatalogStatus:        catalogStatus,
		DiscoveryError:       discoverErr,
	}
	var feedCatalog []modelinfo.Entry
	if feedUsable {
		if feedKey, mapped := modelfeed.FeedKeyForKind(kind); mapped {
			feedCatalog = eligibleCatalogEntries(kind, doc, feedKey, local)
		}
	}

	// Bedrock inference-profile ids are the callable surface. The feed is
	// metadata authority for the routed foundation model, never an independent
	// list of ids to expose: many foundation-model ids are not directly usable
	// with Converse and account access is profile-specific.
	if kind == "bedrock" {
		out.CatalogAuthoritative = false
		switch {
		case discoverErr != nil:
			out.DiscoveryStatus = DiscoveryStatusError
		case len(discovered) == 0:
			out.DiscoveryStatus = DiscoveryStatusEmpty
		default:
			out.DiscoveryStatus = DiscoveryStatusOK
			out.Models = mergeBedrockInferenceProfiles(local, discovered, feedCatalog)
		}
		return out
	}

	// Azure ids are customer deployment names, never global catalog model ids.
	// Explicit local rows remain usable when ARM discovery is unavailable. Feed
	// rows enrich a deployment only through its priced_as underlying model.
	if kind == "azure" {
		out.CatalogAuthoritative = false
		switch {
		case discoverErr != nil:
			out.DiscoveryStatus = DiscoveryStatusError
			out.Models = nil
		case len(discovered) == 0:
			out.DiscoveryStatus = DiscoveryStatusEmpty
			out.Models = mergeAzureDeploymentModels(local, nil, feedCatalog)
		default:
			out.DiscoveryStatus = DiscoveryStatusOK
			out.Models = mergeAzureDeploymentModels(local, discovered, feedCatalog)
		}
		return out
	}

	// The feed includes deployment-only rows, so discovery establishes callable ids.
	if kind == "vertex" {
		out.CatalogAuthoritative = false
		switch {
		case discoverErr != nil:
			out.DiscoveryStatus = DiscoveryStatusError
		case len(discovered) == 0:
			out.DiscoveryStatus = DiscoveryStatusEmpty
		default:
			out.DiscoveryStatus = DiscoveryStatusOK
			out.Models = mergeVertexDiscoveredModels(local, discovered, feedCatalog, feedUsable)
		}
		return out
	}

	// Vertex Express needs two independent facts before a model is assignable:
	// native discovery proves the API key can see a standard generateContent
	// model, while the feed proves that model produces conversation text.
	if kind == "vertex-express" {
		switch {
		case discoverErr != nil:
			out.DiscoveryStatus = DiscoveryStatusError
		case len(discovered) == 0:
			out.DiscoveryStatus = DiscoveryStatusEmpty
		default:
			out.DiscoveryStatus = DiscoveryStatusOK
			out.Models = intersectTypedDiscoveryWithCatalog(feedCatalog, local, discovered)
		}
		return out
	}

	if authoritative {
		feedKey, _ := modelfeed.FeedKeyForKind(kind)
		base := eligibleCatalogEntries(kind, doc, feedKey, local)
		out.Models = base
		switch {
		case discoverErr != nil:
			out.DiscoveryStatus = DiscoveryStatusError
		case len(discovered) == 0:
			out.DiscoveryStatus = DiscoveryStatusEmpty
		default:
			out.DiscoveryStatus = DiscoveryStatusOK
			out.Models = mergeCatalogWithDiscovery(kind, out.Models, local, discovered)
		}
		sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].ID < out.Models[j].ID })
		return out
	}

	// Discovery-authoritative (unmapped, transport-authoritative, or mapped
	// while catalog unavailable).
	switch {
	case discoverErr != nil:
		out.DiscoveryStatus = DiscoveryStatusError
		out.Models = nil
	case len(discovered) == 0:
		out.DiscoveryStatus = DiscoveryStatusEmpty
		out.Models = nil
	default:
		out.DiscoveryStatus = DiscoveryStatusOK
		if discoveryAllUntyped(discovered) {
			out.Models = untypedAllowlistIntersect(kind, local, discovered)
		} else {
			overlays := append(modelinfo.CloneEntries(feedCatalog), modelinfo.CloneEntries(local)...)
			out.Models = applyDiscoveredModels(kind, overlays, filterTypedDiscovery(discovered))
		}
	}
	return out
}

func mergeVertexDiscoveredModels(local, discovered, feedCatalog []modelinfo.Entry, feedUsable bool) []modelinfo.Entry {
	out := make([]modelinfo.Entry, 0, len(discovered))
	for _, model := range discovered {
		if model.Untyped || strings.TrimSpace(model.ID) == "" {
			continue
		}
		entry := model
		lookupID := strings.TrimPrefix(model.ID, "google/")
		if feed, ok := catalogPricingForID("vertex", feedCatalog, lookupID); ok {
			entry = enrichFromDiscovery(feed, model)
			entry.ID = model.ID
			entry.PricedAs = lookupID
		} else if feedUsable {
			// A healthy feed not recognizing the discovered row means there is no
			// positive conversation metadata for this transport.
			continue
		}
		entry = mergeDiscoveredModelEntry("vertex", local, entry)
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func mergeBedrockInferenceProfiles(local, discovered, feedCatalog []modelinfo.Entry) []modelinfo.Entry {
	out := make([]modelinfo.Entry, 0, len(discovered))
	for _, profile := range discovered {
		profileID := strings.TrimSpace(profile.ID)
		if profileID == "" || profile.Untyped {
			continue
		}
		lookupID := strings.TrimSpace(profile.PricedAs)
		if lookupID == "" {
			lookupID = profileID
		}
		entry, feedMatch := catalogPricingForID("bedrock", feedCatalog, lookupID)
		if feedMatch {
			entry = enrichFromDiscovery(entry, profile)
			entry.ID = profileID
			entry.PricedAs = profile.PricedAs
		} else {
			localEntry, declared := declaredModelEntry("bedrock", local, profileID)
			if !declared {
				continue
			}
			entry = overlayLocalModel(profile, localEntry)
		}
		entry = mergeDiscoveredModelEntry("bedrock", local, entry)
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func declaredModelEntry(kind string, models []modelinfo.Entry, id string) (modelinfo.Entry, bool) {
	for _, model := range models {
		if modelinfo.EquivalentID(kind, model.ID, id) {
			return model, true
		}
	}
	return modelinfo.Entry{}, false
}

func intersectTypedDiscoveryWithCatalog(catalog, local, discovered []modelinfo.Entry) []modelinfo.Entry {
	out := make([]modelinfo.Entry, 0, len(discovered))
	for _, model := range discovered {
		if model.Untyped || strings.TrimSpace(model.ID) == "" {
			continue
		}
		eligible, ok := catalogPricingForID("vertex-express", catalog, model.ID)
		if !ok {
			continue
		}
		eligible = enrichFromDiscovery(eligible, model)
		eligible = mergeDiscoveredModelEntry("vertex-express", local, eligible)
		out = append(out, eligible)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func mergeAzureDeploymentModels(local, discovered, feedCatalog []modelinfo.Entry) []modelinfo.Entry {
	byID := make(map[string]modelinfo.Entry, len(local)+len(discovered))
	for _, model := range local {
		if id := strings.TrimSpace(model.ID); id != "" {
			byID[id] = model
		}
	}
	for _, model := range discovered {
		id := strings.TrimSpace(model.ID)
		if id == "" || model.Untyped {
			continue
		}
		if existing, ok := byID[id]; ok {
			model = overlayLocalModel(model, existing)
		}
		byID[id] = model
	}
	out := make([]modelinfo.Entry, 0, len(byID))
	for _, model := range byID {
		lookupID := model.ID
		if model.PricedAs != "" {
			lookupID = model.PricedAs
		}
		if priced, ok := catalogPricingForID("azure", feedCatalog, lookupID); ok {
			priced = overlayLocalModel(priced, model)
			priced.ID = model.ID
			priced.PricedAs = model.PricedAs
			model = priced
		}
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// mergeCatalogWithDiscovery enriches/appends onto an eligible catalog base.
// Untyped live ids must already be in the eligible set; typed live ids may append.
func mergeCatalogWithDiscovery(
	kind string,
	base []modelinfo.Entry,
	local []modelinfo.Entry,
	discovered []modelinfo.Entry,
) []modelinfo.Entry {
	byID := make(map[string]modelinfo.Entry, len(base))
	order := make([]string, 0, len(base)+len(discovered))
	for _, m := range base {
		byID[m.ID] = m
		order = append(order, m.ID)
	}
	for _, d := range discovered {
		id := strings.TrimSpace(d.ID)
		if id == "" {
			continue
		}
		if existing, ok := byID[id]; ok {
			byID[id] = enrichFromDiscovery(existing, d)
			continue
		}
		if matched, ok := lookupEquivalent(kind, byID, id); ok {
			byID[matched] = enrichFromDiscovery(byID[matched], d)
			continue
		}
		if d.Untyped {
			// Fail-closed: untyped live-only ids never enter assignable.
			continue
		}
		// Typed discovery may append conversation-filtered live ids.
		entry := mergeDiscoveredModelEntry(kind, local, d)
		byID[entry.ID] = entry
		order = append(order, entry.ID)
	}
	out := make([]modelinfo.Entry, 0, len(order))
	seen := make(map[string]struct{}, len(order))
	for _, id := range order {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if m, ok := byID[id]; ok {
			out = append(out, m)
		}
	}
	return out
}

func enrichFromDiscovery(base, discovered modelinfo.Entry) modelinfo.Entry {
	out := base
	if discovered.DiscoveredThinking != nil {
		out.DiscoveredThinking = discovered.DiscoveredThinking.Clone()
		out.DiscoveredThinkStyle = discovered.DiscoveredThinkStyle
	}
	if discovered.ContextLength > 0 {
		out.ContextLength = discovered.ContextLength
	}
	// The live transport's own output ceiling outranks the feed's.
	if discovered.MaxTokens > 0 {
		out.MaxTokens = discovered.MaxTokens
	}
	if discovered.ThinkingAlwaysOn {
		out.ThinkingAlwaysOn = true
	}
	out.Capabilities = modelinfo.MergeCapabilities(out.Capabilities, discovered.Capabilities)
	// Live discovery owns callability.
	out.Callable = modelinfo.OverlayEvidence(out.Callable, discovered.Callable)
	if out.PricedAs == "" && discovered.PricedAs != "" {
		out.PricedAs = discovered.PricedAs
	}
	if discovered.DiscoveredPricing != nil && discovered.PriceProvenance == modelinfo.PriceProvenanceDiscovered {
		modelinfo.ApplyDiscoveredRate(&out, *discovered.DiscoveredPricing)
	}
	return out
}

func lookupEquivalent(kind string, byID map[string]modelinfo.Entry, id string) (string, bool) {
	for existing := range byID {
		if modelinfo.EquivalentID(kind, existing, id) {
			return existing, true
		}
	}
	return "", false
}

func discoveryAllUntyped(discovered []modelinfo.Entry) bool {
	if len(discovered) == 0 {
		return false
	}
	for _, d := range discovered {
		if !d.Untyped {
			return false
		}
	}
	return true
}

func filterTypedDiscovery(discovered []modelinfo.Entry) []modelinfo.Entry {
	out := make([]modelinfo.Entry, 0, len(discovered))
	for _, d := range discovered {
		if d.Untyped {
			continue
		}
		out = append(out, d)
	}
	return out
}

// untypedAllowlistIntersect keeps id-only discovery empty unless
// the user listed explicit ids in local models[]; those must also appear live.
func untypedAllowlistIntersect(kind string, local, discovered []modelinfo.Entry) []modelinfo.Entry {
	if len(local) == 0 {
		return nil
	}
	allow := modelIDSet(local)
	kept := make([]modelinfo.Entry, 0)
	for _, d := range discovered {
		id := strings.TrimSpace(d.ID)
		if id == "" {
			continue
		}
		if _, ok := allow[id]; ok {
			kept = append(kept, d)
			continue
		}
		for lid := range allow {
			if modelinfo.EquivalentID(kind, lid, id) {
				kept = append(kept, d)
				break
			}
		}
	}
	return applyDiscoveredModels(kind, local, kept)
}

func modelIDSet(models []modelinfo.Entry) map[string]struct{} {
	out := make(map[string]struct{}, len(models))
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		out[id] = struct{}{}
	}
	return out
}

func eligibleCatalogEntries(kind string, doc *modelfeed.Document, feedKey string, local []modelinfo.Entry) []modelinfo.Entry {
	if doc == nil || strings.TrimSpace(feedKey) == "" {
		return nil
	}
	eligible := doc.EligibleModels(feedKey)
	localByID := make(map[string]modelinfo.Entry, len(local))
	for _, m := range local {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		localByID[id] = m
	}
	out := make([]modelinfo.Entry, 0, len(eligible))
	for _, fm := range eligible {
		entry := modelEntryFromFeed(fm)
		if override, ok := localByID[entry.ID]; ok {
			entry = overlayLocalModel(entry, override)
		} else {
			for _, override := range local {
				if modelinfo.EquivalentID(kind, override.ID, entry.ID) {
					entry = overlayLocalModel(entry, override)
					break
				}
			}
		}
		out = append(out, entry)
	}
	return out
}

// feedOutputLimit reads a feed row's output ceiling. A ceiling that is not
// smaller than the context window leaves no room for a prompt, so it is not a
// ceiling a request budget can use.
func feedOutputLimit(limit modelfeed.Limit) int {
	if limit.Output <= 0 {
		return 0
	}
	if limit.Context > 0 && limit.Output >= limit.Context {
		return 0
	}
	return limit.Output
}

func modelEntryFromFeed(m modelfeed.Model) modelinfo.Entry {
	e := modelinfo.Entry{ID: m.ID, Capabilities: modelinfo.ModelCapabilities{
		Chat:      modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev"),
		Streaming: modelinfo.Evidence(modelinfo.CapabilityUnknown, "models.dev"),
		Tools:     modelinfo.Evidence(modelinfo.OptionalState(m.ToolCall), "models.dev"),
		Reasoning: modelinfo.Evidence(modelinfo.OptionalState(m.Reasoning), "models.dev"),
	}}
	if m.StructuredOutput != nil {
		e.Capabilities.StructuredOutput = modelinfo.Evidence(modelinfo.State(*m.StructuredOutput), "models.dev")
	}
	if m.Cost != nil {
		// Feed cost.* is USD per million tokens; ModelEntry stores per-1k hints.
		if m.Cost.Input != nil {
			e.InputPer1K = *m.Cost.Input / 1000
		}
		if m.Cost.Output != nil {
			e.OutputPer1K = *m.Cost.Output / 1000
		}
		e.Currency = "USD"
		e.PriceProvenance = modelinfo.PriceProvenanceCatalog
	}
	if m.Limit != nil {
		if m.Limit.Context > 0 {
			e.ContextLength = m.Limit.Context
		}
		e.MaxTokens = feedOutputLimit(*m.Limit)
	}
	if m.Modalities.Input != nil {
		e.Capabilities.Vision = modelinfo.Evidence(modelinfo.CapabilityUnsupported, "models.dev")
	}
	for _, in := range m.Modalities.Input {
		if strings.EqualFold(strings.TrimSpace(in), "image") {
			e.Capabilities.Vision = modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev")
			break
		}
	}
	return e
}

func overlayLocalModel(base, local modelinfo.Entry) modelinfo.Entry {
	out := base
	if local.Thinking != nil {
		out.Thinking = local.Thinking.Clone()
	}
	if local.DiscoveredThinking != nil {
		out.DiscoveredThinking = local.DiscoveredThinking.Clone()
		out.DiscoveredThinkStyle = local.DiscoveredThinkStyle
	}
	out.ID = base.ID
	if out.DiscoveredPricing == nil && local.InputPer1K != 0 {
		out.InputPer1K = local.InputPer1K
		out.PriceProvenance = modelinfo.PriceProvenanceCatalog
	}
	if out.DiscoveredPricing == nil && local.OutputPer1K != 0 {
		out.OutputPer1K = local.OutputPer1K
		out.PriceProvenance = modelinfo.PriceProvenanceCatalog
	}
	if out.DiscoveredPricing == nil && local.Currency != "" {
		out.Currency = local.Currency
	}
	if local.Temperature != nil {
		out.Temperature = local.Temperature
	}
	if local.MaxTokens != 0 {
		out.MaxTokens = local.MaxTokens
	}
	if local.ReasoningEffort != "" {
		out.ReasoningEffort = local.ReasoningEffort
	}
	if local.ReasoningEffortLevels != (modelinfo.ReasoningEffortLevels{}) {
		out.ReasoningEffortLevels = local.ReasoningEffortLevels
	}
	if local.ContextLength != 0 {
		out.ContextLength = local.ContextLength
	}
	if local.ThinkingAlwaysOn {
		out.ThinkingAlwaysOn = true
	}
	if local.ThinkStyle != "" {
		out.ThinkStyle = local.ThinkStyle
	}
	out.Capabilities = modelinfo.MergeCapabilities(out.Capabilities, local.Capabilities)
	if local.InputNeuronsPerM != 0 {
		out.InputNeuronsPerM = local.InputNeuronsPerM
	}
	if local.OutputNeuronsPerM != 0 {
		out.OutputNeuronsPerM = local.OutputNeuronsPerM
	}
	if local.PricedAs != "" {
		out.PricedAs = local.PricedAs
	}
	return out
}
