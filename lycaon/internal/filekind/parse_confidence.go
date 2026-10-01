package filekind

import (
	"context"
	"sort"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/lycaon/lycaon/internal/tsparse"
)

func tryParseConfidence(ctx context.Context, sample []byte) (*grammars.LangEntry, bool) {
	sample = truncateHeadSample(sample)
	if len(sample) == 0 {
		return nil, false
	}
	candidates := nominateParseCandidates(sample)
	if len(candidates) == 0 {
		return nil, false
	}
	type scored struct {
		name string
		conf float64
	}
	scores := make([]scored, 0, len(candidates))
	for _, name := range candidates {
		conf, ok := parseConfidenceScore(ctx, name, sample)
		if !ok || conf < ParseConfidenceFloor {
			continue
		}
		scores = append(scores, scored{name: name, conf: conf})
	}
	if len(scores) == 0 {
		return nil, false
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].conf != scores[j].conf {
			return scores[i].conf > scores[j].conf
		}
		return scores[i].name < scores[j].name
	})
	if len(scores) >= 2 && scores[0].conf-scores[1].conf < parseConfidenceTieE {
		return nil, false
	}
	entry := grammars.DetectLanguageByName(scores[0].name)
	if entry == nil {
		return nil, false
	}
	return entry, true
}

func parseConfidenceScore(ctx context.Context, langName string, sample []byte) (float64, bool) {
	entry := grammars.DetectLanguageByName(langName)
	if entry == nil || entry.Language == nil {
		return 0, false
	}
	errors, ok := countParseErrors(ctx, entry.Language(), sample)
	if !ok {
		return 0, false
	}
	lines := nonEmptyLineCount(sample)
	if lines == 0 {
		lines = 1
	}
	errorRatio := float64(errors) / float64(lines)
	if errorRatio > 1 {
		errorRatio = 1
	}
	return 1 - errorRatio, true
}

// Partial trees cannot establish a whole-sample error ratio.
func countParseErrors(ctx context.Context, lang *gotreesitter.Language, src []byte) (int, bool) {
	defer func() {
		_ = recover()
	}()
	tree, err := tsparse.Parse(ctx, lang, src, tsparse.Analysis)
	if err != nil {
		return 0, false
	}
	defer tree.Release()
	var count int
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if n == nil || count >= maxParseCandidates*4 {
			return
		}
		if n.IsError() || n.IsMissing() {
			count++
			return
		}
		for i := 0; i < n.ChildCount(); i++ {
			walk(n.Child(i))
		}
	}
	walk(tree.RootNode())
	return count, true
}

// Candidate limits bound grammar initialization as well as parsing.
func nominateParseCandidates(sample []byte) []string {
	text := string(truncateHeadSample(sample))
	var out []string
	add := func(name string) {
		if len(out) >= maxParseCandidates {
			return
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return
		}
		for _, existing := range out {
			if existing == name {
				return
			}
		}
		if grammars.DetectLanguageByName(name) == nil {
			return
		}
		out = append(out, name)
	}
	if strings.Contains(text, "package ") && strings.Contains(text, "func ") {
		add("go")
	}
	if strings.Contains(text, "def ") || (strings.Contains(text, "import ") && strings.Contains(text, ":")) {
		add("python")
	}
	if strings.Contains(text, "fn ") && strings.Contains(text, "{") {
		add("rust")
	}
	if strings.Contains(text, "{") && strings.Contains(text, ";") {
		add("javascript")
		add("typescript")
		add("rust")
	}
	if strings.Contains(text, "<!DOCTYPE") || strings.Contains(text, "<html") {
		add("html")
	}
	sort.Strings(out)
	if len(out) > maxParseCandidates {
		out = out[:maxParseCandidates]
	}
	return out
}
