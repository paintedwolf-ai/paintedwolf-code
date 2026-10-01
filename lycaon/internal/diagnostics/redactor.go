package diagnostics

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/webresearch"
)

// Redactor combines baseline scrubbing with catalog-backed secret matching.
type Redactor struct {
	matcher            *secretmatch.Matcher
	providerAssignment *regexp.Regexp
}

// NewRedactor builds the artifact redactor from bundled catalogs.
func NewRedactor() (*Redactor, error) {
	names, err := providerSecretNames()
	if err != nil {
		return nil, err
	}
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	if err != nil {
		return nil, fmt.Errorf("diagnostics secret matcher: %w", err)
	}
	return &Redactor{
		matcher:            matcher,
		providerAssignment: providerAssignmentRE(names),
	}, nil
}

// RedactText removes structured and catalog-backed secret material.
func (r *Redactor) RedactText(ctx context.Context, s string) string {
	out := observability.RedactString(s)
	if r == nil {
		return out
	}
	if r.providerAssignment != nil {
		out = r.providerAssignment.ReplaceAllString(out, "${1}[REDACTED]")
	}
	if r.matcher != nil {
		out = r.matcher.RedactString(ctx, out)
	}
	return out
}

func providerSecretNames() ([]string, error) {
	llmConfig, err := llm.LoadProviderConfig()
	if err != nil {
		return nil, fmt.Errorf("diagnostics model providers: %w", err)
	}
	webCatalog, err := webresearch.LoadCatalog()
	if err != nil {
		return nil, fmt.Errorf("diagnostics web research providers: %w", err)
	}
	set := make(map[string]struct{})
	for _, provider := range llmConfig.Providers {
		if name := strings.TrimSpace(provider.APIKeyEnv); name != "" {
			set[name] = struct{}{}
		}
	}
	for _, provider := range webCatalog.Entries() {
		if name := strings.TrimSpace(provider.APIKeyEnv); name != "" {
			set[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func providerAssignmentRE(names []string) *regexp.Regexp {
	if len(names) == 0 {
		return nil
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, regexp.QuoteMeta(name))
	}
	return regexp.MustCompile(`(?i)((?:["']?(?:` + strings.Join(parts, "|") + `)["']?)\s*[=:]\s*)(?:"[^"]*"|'[^']*'|[^\s,}]+)`)
}
