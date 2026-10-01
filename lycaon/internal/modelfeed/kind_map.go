package modelfeed

import "strings"

// feedProviderKeyByKind maps provider kinds to feed keys.
var feedProviderKeyByKind = map[string]string{
	"openai":     "openai",
	"anthropic":  "anthropic",
	"together":   "togetherai",
	"openrouter": "openrouter",
	"fireworks":  "fireworks-ai",
	"bedrock":    "amazon-bedrock",
	"vertex":     "google-vertex",
	"gemini":     "google",
	// Express transport uses the single-publisher catalog.
	"vertex-express":        "google",
	"azure":                 "azure",
	"cloudflare-workers-ai": "cloudflare-workers-ai",
}

// Shared feed keys use an explicit canonical kind to avoid map-order choices.
var canonicalKindByFeedKey = map[string]string{
	"google": "gemini",
}

var feedKeyToKind = func() map[string]string {
	out := make(map[string]string, len(feedProviderKeyByKind))
	for kind, key := range feedProviderKeyByKind {
		lower := strings.ToLower(key)
		if canonical, ok := canonicalKindByFeedKey[lower]; ok {
			out[lower] = canonical
			continue
		}
		out[lower] = kind
	}
	return out
}()

// FeedKeyForKind returns the feed key for a provider kind.
func FeedKeyForKind(kind string) (string, bool) {
	key, ok := feedProviderKeyByKind[strings.TrimSpace(kind)]
	return key, ok
}

// KindForFeedKey returns the provider kind for a feed key.
func KindForFeedKey(feedKey string) (string, bool) {
	kind, ok := feedKeyToKind[strings.ToLower(strings.TrimSpace(feedKey))]
	return kind, ok
}

// MappedKinds returns provider kinds with an explicit feed-key mapping.
func MappedKinds() []string {
	out := make([]string, 0, len(feedProviderKeyByKind))
	for kind := range feedProviderKeyByKind {
		out = append(out, kind)
	}
	return out
}
