package secretmatch

import (
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"unicode"

	gconfig "github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
)

func buildOCRDetector(base gconfig.Config, c ocrConfusionCatalog) *detect.Detector {
	cfg := base
	cfg.Rules = make(map[string]gconfig.Rule, len(base.Rules))
	for id, rule := range base.Rules {
		if rule.Regex != nil {
			if expanded, err := expandOCRRegex(rule.Regex.String(), c); err == nil {
				rule.Regex = expanded
			}
		}
		if len(rule.Keywords) > 0 {
			rule.Keywords = expandOCRKeywords(rule.Keywords, c)
		}
		if len(rule.Allowlists) > 0 {
			allows := make([]*gconfig.Allowlist, len(rule.Allowlists))
			for i, a := range rule.Allowlists {
				if a == nil {
					continue
				}
				clone := *a
				if len(a.Regexes) > 0 {
					clone.Regexes = make([]*regexp.Regexp, len(a.Regexes))
					for j, re := range a.Regexes {
						if re != nil {
							if exp, err := expandOCRRegex(re.String(), c); err == nil {
								clone.Regexes[j] = exp
								continue
							}
						}
						clone.Regexes[j] = re
					}
				}
				if len(a.StopWords) > 0 {
					clone.StopWords = expandOCRKeywords(a.StopWords, c)
				}
				allows[i] = &clone
			}
			rule.Allowlists = allows
		}
		cfg.Rules[id] = rule
	}
	cfg.OrderedRules = append([]string(nil), base.OrderedRules...)
	rebuildConfigIndexes(&cfg)
	built := detect.NewDetector(cfg)
	built.MaxDecodeDepth = 2
	return built
}

func expandOCRRegex(pattern string, c ocrConfusionCatalog) (*regexp.Regexp, error) {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	expanded := expandSyntax(parsed, c)
	return regexp.Compile(expanded.String())
}

func expandSyntax(re *syntax.Regexp, c ocrConfusionCatalog) *syntax.Regexp {
	if re == nil {
		return nil
	}
	out := *re
	if len(re.Sub) > 0 {
		out.Sub = make([]*syntax.Regexp, len(re.Sub))
		for i, sub := range re.Sub {
			out.Sub[i] = expandSyntax(sub, c)
		}
	}
	switch re.Op {
	case syntax.OpLiteral:
		return expandLiteral(&out, c)
	case syntax.OpCharClass:
		return expandCharClass(&out, c)
	default:
		return &out
	}
}

func expandLiteral(re *syntax.Regexp, c ocrConfusionCatalog) *syntax.Regexp {
	fold := re.Flags&syntax.FoldCase != 0
	needsExpand := false
	for _, r := range re.Rune {
		if len(c.literalPartners(r, fold)) > 1 {
			needsExpand = true
			break
		}
	}
	if !needsExpand {
		return re
	}
	var parts []*syntax.Regexp
	var litRunes []rune
	flushLit := func() {
		if len(litRunes) == 0 {
			return
		}
		cp := append([]rune(nil), litRunes...)
		parts = append(parts, &syntax.Regexp{
			Op:    syntax.OpLiteral,
			Flags: re.Flags,
			Rune:  cp,
		})
		litRunes = litRunes[:0]
	}
	for _, r := range re.Rune {
		partners := c.literalPartners(r, fold)
		if len(partners) <= 1 {
			litRunes = append(litRunes, r)
			continue
		}
		flushLit()
		parts = append(parts, &syntax.Regexp{
			Op:    syntax.OpCharClass,
			Flags: re.Flags &^ syntax.FoldCase,
			Rune:  addRunesToCharClass(nil, partners),
		})
	}
	flushLit()
	if len(parts) == 1 {
		return parts[0]
	}
	return &syntax.Regexp{
		Op:    syntax.OpConcat,
		Flags: re.Flags,
		Sub:   parts,
	}
}

func (c ocrConfusionCatalog) literalPartners(r rune, fold bool) []rune {
	if !fold {
		if p := c.partners[r]; len(p) > 0 {
			return p
		}
		return nil
	}
	candidates := []rune{r, unicode.ToLower(r), unicode.ToUpper(r)}
	set := map[rune]struct{}{}
	confused := false
	for _, cand := range candidates {
		set[cand] = struct{}{}
		if p := c.partners[cand]; len(p) > 0 {
			confused = true
			for _, pr := range p {
				set[pr] = struct{}{}
				set[unicode.ToLower(pr)] = struct{}{}
				set[unicode.ToUpper(pr)] = struct{}{}
			}
		}
	}
	if !confused {
		return nil
	}
	out := make([]rune, 0, len(set))
	for pr := range set {
		out = append(out, pr)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

func expandCharClass(re *syntax.Regexp, c ocrConfusionCatalog) *syntax.Regexp {
	if len(re.Rune) == 0 || len(re.Rune)%2 != 0 || re.Rune[len(re.Rune)-1] == unicode.MaxRune {
		return re
	}
	if charClassContains(re.Rune, ' ') || charClassContains(re.Rune, '\n') {
		return re
	}
	var added []rune
	for _, cls := range c.classes {
		hit := false
		for _, g := range cls {
			if charClassContains(re.Rune, g) {
				hit = true
				break
			}
		}
		if hit {
			added = append(added, cls...)
		}
	}
	if len(added) == 0 {
		return re
	}
	re.Rune = addRunesToCharClass(re.Rune, added)
	return re
}

func charClassContains(ranges []rune, r rune) bool {
	for i := 0; i+1 < len(ranges); i += 2 {
		if r >= ranges[i] && r <= ranges[i+1] {
			return true
		}
	}
	return false
}

func addRunesToCharClass(existing, added []rune) []rune {
	intervals := make([][2]rune, 0, len(existing)/2+len(added))
	for i := 0; i+1 < len(existing); i += 2 {
		intervals = append(intervals, [2]rune{existing[i], existing[i+1]})
	}
	for _, r := range added {
		intervals = append(intervals, [2]rune{r, r})
	}
	sort.Slice(intervals, func(a, b int) bool {
		if intervals[a][0] != intervals[b][0] {
			return intervals[a][0] < intervals[b][0]
		}
		return intervals[a][1] < intervals[b][1]
	})
	merged := intervals[:0]
	for _, iv := range intervals {
		if n := len(merged); n > 0 && iv[0] <= merged[n-1][1]+1 {
			if iv[1] > merged[n-1][1] {
				merged[n-1][1] = iv[1]
			}
			continue
		}
		merged = append(merged, iv)
	}
	out := make([]rune, 0, len(merged)*2)
	for _, iv := range merged {
		out = append(out, iv[0], iv[1])
	}
	return out
}

func expandOCRKeywords(keywords []string, c ocrConfusionCatalog) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		if _, dup := seen[s]; !dup {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	for _, kw := range keywords {
		lower := strings.ToLower(kw)
		for _, v := range keywordVariants(lower, c) {
			add(v)
		}
	}
	return out
}

func keywordVariants(lower string, c ocrConfusionCatalog) []string {
	runes := []rune(lower)
	results := []string{lower}
	for i, r := range runes {
		partners := c.lowerPartners[r]
		if len(partners) <= 1 {
			continue
		}
		if len(results)*len(partners) <= maxOCRKeywordVariants {
			next := make([]string, 0, len(results)*len(partners))
			for _, prefix := range results {
				pr := []rune(prefix)
				for _, p := range partners {
					pr[i] = p
					next = append(next, string(pr))
				}
			}
			results = next
			continue
		}
		for _, p := range partners {
			if p == r || len(results) >= maxOCRKeywordVariants {
				continue
			}
			cp := append([]rune(nil), runes...)
			cp[i] = p
			results = append(results, string(cp))
		}
	}
	return results
}
