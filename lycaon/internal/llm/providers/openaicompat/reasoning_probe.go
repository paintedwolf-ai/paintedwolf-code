package openaicompat

import (
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

// reasoningProbeTTL bounds cached control rejections.
const reasoningProbeTTL = 30 * time.Minute

// reasoningFallback selects a provider-declared retry rung.
type reasoningFallback int

const (
	// reasoningFallbackNone sends the resolved reasoning controls.
	reasoningFallbackNone reasoningFallback = iota
	// reasoningFallbackOmit drops the reasoning control field entirely.
	reasoningFallbackOmit
	// reasoningFallbackOff sends the declared disable token.
	reasoningFallbackOff
)

var reasoningUnsupported sync.Map // providerModelKey -> reasoningProbeEntry

type providerModelKey struct {
	providerID string
	model      string
}

type reasoningProbeEntry struct {
	fallback reasoningFallback
	expiry   time.Time
}

// markReasoningFallback caches the strongest recent rung per route.
func markReasoningFallback(providerID, model string, fallback reasoningFallback) {
	key := providerModelKey{providerID: strings.TrimSpace(providerID), model: strings.TrimSpace(model)}
	if key.providerID == "" || key.model == "" || fallback == reasoningFallbackNone {
		return
	}
	if cached := cachedReasoningFallback(key.providerID, key.model); cached > fallback {
		return
	}
	reasoningUnsupported.Store(key, reasoningProbeEntry{
		fallback: fallback,
		expiry:   time.Now().Add(reasoningProbeTTL),
	})
}

// cachedReasoningFallback reports probe-and-cache state for provider/model.
func cachedReasoningFallback(providerID, model string) reasoningFallback {
	key := providerModelKey{providerID: strings.TrimSpace(providerID), model: strings.TrimSpace(model)}
	v, ok := reasoningUnsupported.Load(key)
	if !ok {
		return reasoningFallbackNone
	}
	entry, ok := v.(reasoningProbeEntry)
	if !ok || time.Now().After(entry.expiry) {
		reasoningUnsupported.Delete(key)
		return reasoningFallbackNone
	}
	return entry.fallback
}

// nextReasoningFallback advances the declared retry ladder.
func nextReasoningFallback(err error, current reasoningFallback, hasOffToken bool) (reasoningFallback, bool) {
	if !providerretry.IsRequestRejected(err) {
		return current, false
	}
	switch current {
	case reasoningFallbackNone:
		return reasoningFallbackOmit, true
	case reasoningFallbackOmit:
		if hasOffToken {
			return reasoningFallbackOff, true
		}
		return current, false
	default:
		return current, false
	}
}

// ResetReasoningProbeForTest clears probe cache (tests only).
func ResetReasoningProbeForTest() {
	reasoningUnsupported = sync.Map{}
}
