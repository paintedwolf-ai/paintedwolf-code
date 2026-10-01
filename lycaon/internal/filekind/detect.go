// Package filekind classifies source, logs, and plain text.
package filekind

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/odvcencio/gotreesitter/grammars"
)

// DepthMode controls how many detection tiers may run.
type DepthMode int

const (
	// DepthShallow runs tiers 1–2 (extension + shebang) for repo-wide walks.
	DepthShallow DepthMode = iota
	// DepthDeep runs tiers 1–5 (adds parse-confidence + log classify) for single-file read.
	DepthDeep
)

const (
	TierExtension       = 1
	TierShebang         = 2
	TierParseConfidence = 3
	TierLogClassifier   = 4
	TierRegexDegraded   = 5
)

const (
	LogClassifyThreshold = 0.85
	ParseConfidenceFloor = 0.90
)

// DetectReq names the inputs for one Detect call.
type DetectReq struct {
	Filename   string
	HeadSample []byte
	Mode       DepthMode
}

// DetectResult names the winning tier and exactly one outcome.
type DetectResult struct {
	Tier    int
	Grammar *grammars.LangEntry
	Log     logoutline.LogFormat
}

// Detect selects the first matching detection tier.
func Detect(ctx context.Context, req DetectReq) DetectResult {
	name := strings.TrimSpace(req.Filename)
	if name == "" {
		return DetectResult{Tier: TierRegexDegraded}
	}
	if entry := GrammarForPath(filepath.Base(name)); entry != nil {
		return DetectResult{Tier: TierExtension, Grammar: entry}
	}
	if line := firstLine(req.HeadSample); line != "" {
		if entry := grammars.DetectLanguageByShebang(line); entry != nil {
			return DetectResult{Tier: TierShebang, Grammar: entry}
		}
	}
	if req.Mode == DepthDeep {
		if entry, ok := tryParseConfidence(ctx, req.HeadSample); ok {
			return DetectResult{Tier: TierParseConfidence, Grammar: entry}
		}
		if format, conf := logoutline.Classify(truncateHeadSample(req.HeadSample)); format != logoutline.FormatNone && conf >= LogClassifyThreshold {
			return DetectResult{Tier: TierLogClassifier, Log: format}
		}
	}
	return DetectResult{Tier: TierRegexDegraded}
}

// GrammarForPath resolves filename metadata without parsing source.
func GrammarForPath(name string) *grammars.LangEntry {
	entry := grammars.DetectLanguage(name)
	if entry == nil && strings.EqualFold(filepath.Ext(name), ".gradle") {
		entry = grammars.DetectLanguageByName("groovy")
	}
	if entry == nil {
		return nil
	}
	// The documentation grammar also claims .txt; ordinary text stays unclassified.
	if strings.EqualFold(filepath.Ext(name), ".txt") && entry.Name == "vimdoc" {
		return nil
	}
	return entry
}
