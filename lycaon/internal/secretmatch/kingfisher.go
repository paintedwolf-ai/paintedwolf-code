package secretmatch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/lycaon/lycaon/config"
	gconfig "github.com/zricethezav/gitleaks/v8/config"
	"gopkg.in/yaml.v3"
)

const (
	kingfisherSource = "kingfisher"
	defaultSpecials  = "!@#$%^&*()_+-=[]{}|;:'\",.<>?/\\`~"
)

type kingfisherManifest struct {
	Source struct {
		Name       string `yaml:"name"`
		Version    string `yaml:"version"`
		Commit     string `yaml:"commit"`
		Repository string `yaml:"repository"`
		License    string `yaml:"license"`
	} `yaml:"source"`
	Policy struct {
		MinimumConfidence  string            `yaml:"minimum_confidence"`
		Validation         string            `yaml:"validation"`
		OutboundExclusions map[string]string `yaml:"outbound_exclusions"`
	} `yaml:"policy"`
	Inventory   kingfisherInventory `yaml:"inventory"`
	RulesDigest string              `yaml:"rules_digest"`
}

type kingfisherInventory struct {
	Files         int               `yaml:"files"`
	UpstreamRules int               `yaml:"upstream_rules"`
	ImportedRules int               `yaml:"imported_rules"`
	Skipped       map[string]int    `yaml:"skipped"`
	ExcludedRules map[string]string `yaml:"excluded_rules"`
}

type kingfisherDocument struct {
	Rules []kingfisherSourceRule `yaml:"rules"`
}

type kingfisherSourceRule struct {
	Name                string                 `yaml:"name"`
	ID                  string                 `yaml:"id"`
	Pattern             string                 `yaml:"pattern"`
	MinEntropy          float64                `yaml:"min_entropy"`
	Confidence          string                 `yaml:"confidence"`
	Visible             *bool                  `yaml:"visible"`
	Examples            []string               `yaml:"examples"`
	NegativeExamples    []string               `yaml:"negative_examples"`
	PatternRequirements kingfisherRequirements `yaml:"pattern_requirements"`
}

type kingfisherRequirements struct {
	MinDigits        *int       `yaml:"min_digits"`
	MinUppercase     *int       `yaml:"min_uppercase"`
	MinLowercase     *int       `yaml:"min_lowercase"`
	MinSpecialChars  *int       `yaml:"min_special_chars"`
	SpecialChars     string     `yaml:"special_chars"`
	IgnoreIfContains []string   `yaml:"ignore_if_contains"`
	Checksum         *yaml.Node `yaml:"checksum"`
}

type kingfisherRule struct {
	id           string
	title        string
	regex        *regexp.Regexp
	keywords     []string
	entropy      float64
	requirements kingfisherRequirements
}

type kingfisherCatalog struct {
	rules              []kingfisherRule
	requirements       map[string]kingfisherRequirements
	outboundExclusions map[string]string
	inventory          kingfisherInventory
	digest             string
	version            string
}

var (
	kingfisherOnce sync.Once
	kingfisherData kingfisherCatalog
	kingfisherErr  error
)

func bundledKingfisherCatalog() (kingfisherCatalog, error) {
	kingfisherOnce.Do(func() {
		kingfisherData, kingfisherErr = loadKingfisherCatalog()
	})
	return kingfisherData, kingfisherErr
}

func loadKingfisherCatalog() (kingfisherCatalog, error) {
	manifestRaw, err := config.Read(config.KingfisherManifest)
	if err != nil {
		return kingfisherCatalog{}, fmt.Errorf("secretmatch: kingfisher manifest: %w", err)
	}
	manifest, err := parseKingfisherManifest(manifestRaw)
	if err != nil {
		return kingfisherCatalog{}, fmt.Errorf("secretmatch: kingfisher manifest: %w", err)
	}

	var files []config.Rel
	err = config.Walk(config.KingfisherCatalogDir, func(path config.Rel, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return kingfisherCatalog{}, fmt.Errorf("secretmatch: walk kingfisher rules: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].String() < files[j].String() })

	catalog := kingfisherCatalog{
		requirements:       make(map[string]kingfisherRequirements),
		outboundExclusions: manifest.Policy.OutboundExclusions,
		inventory: kingfisherInventory{
			Files: len(files), Skipped: map[string]int{}, ExcludedRules: map[string]string{},
		},
	}
	hash := sha256.New()
	seen := make(map[string]string)
	for _, path := range files {
		raw, readErr := config.Read(path)
		if readErr != nil {
			return kingfisherCatalog{}, fmt.Errorf("secretmatch: read %s: %w", path, readErr)
		}
		rel := strings.TrimPrefix(path.String(), config.KingfisherCatalogDir.String()+"/")
		_, _ = io.WriteString(hash, rel+"\n")
		_, _ = hash.Write(raw)
		_, _ = io.WriteString(hash, "\n")

		var doc kingfisherDocument
		// Decode only the fields used by the matcher.
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return kingfisherCatalog{}, fmt.Errorf("secretmatch: parse %s: %w", path, err)
		}
		if len(doc.Rules) == 0 {
			return kingfisherCatalog{}, fmt.Errorf("secretmatch: %s: no rules", path)
		}
		catalog.inventory.UpstreamRules += len(doc.Rules)
		for i := range doc.Rules {
			sourceRule := &doc.Rules[i]
			if previous := seen[sourceRule.ID]; previous != "" {
				return kingfisherCatalog{}, fmt.Errorf("secretmatch: duplicate kingfisher id %q in %s and %s", sourceRule.ID, previous, path)
			}
			seen[sourceRule.ID] = path.String()
			rule, reason, compileErr := compileKingfisherRule(sourceRule)
			if compileErr != nil {
				return kingfisherCatalog{}, fmt.Errorf("secretmatch: %s: %s: %w", path, sourceRule.ID, compileErr)
			}
			if reason != "" {
				catalog.inventory.Skipped[reason]++
				catalog.inventory.ExcludedRules[sourceRule.ID] = reason
				continue
			}
			catalog.rules = append(catalog.rules, rule)
			if !rule.requirements.empty() {
				catalog.requirements[rule.id] = rule.requirements
			}
		}
	}
	catalog.inventory.ImportedRules = len(catalog.rules)
	catalog.digest = hex.EncodeToString(hash.Sum(nil))
	sort.Slice(catalog.rules, func(i, j int) bool { return catalog.rules[i].id < catalog.rules[j].id })
	for id, reason := range catalog.outboundExclusions {
		if strings.TrimSpace(reason) == "" {
			return kingfisherCatalog{}, fmt.Errorf("secretmatch: kingfisher outbound exclusion %q has no reason", id)
		}
		if _, ok := catalogRuleByID(catalog.rules, id); !ok {
			return kingfisherCatalog{}, fmt.Errorf("secretmatch: kingfisher outbound exclusion %q is not an imported rule", id)
		}
	}
	if err := validateKingfisherInventory(manifest, catalog); err != nil {
		return kingfisherCatalog{}, err
	}
	catalog.version = manifest.Source.Version
	return catalog, nil
}

func catalogRuleByID(rules []kingfisherRule, id string) (kingfisherRule, bool) {
	i := sort.Search(len(rules), func(i int) bool { return rules[i].id >= id })
	if i < len(rules) && rules[i].id == id {
		return rules[i], true
	}
	return kingfisherRule{}, false
}

func parseKingfisherManifest(raw []byte) (kingfisherManifest, error) {
	var manifest kingfisherManifest
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return kingfisherManifest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return kingfisherManifest{}, err
		}
		return kingfisherManifest{}, errors.New("multiple YAML documents are not supported")
	}
	if manifest.Source.Name != kingfisherSource || manifest.Source.Version == "" || manifest.Source.Commit == "" ||
		manifest.Source.Repository == "" || manifest.Source.License != "Apache-2.0" {
		return kingfisherManifest{}, errors.New("source identity, pin, repository, and Apache-2.0 license are required")
	}
	if manifest.Policy.MinimumConfidence != "medium" || manifest.Policy.Validation != "offline-only" {
		return kingfisherManifest{}, errors.New("policy must require medium confidence and offline-only validation")
	}
	if manifest.RulesDigest == "" {
		return kingfisherManifest{}, errors.New("rules_digest is required")
	}
	return manifest, nil
}

func validateKingfisherInventory(manifest kingfisherManifest, catalog kingfisherCatalog) error {
	got, want := catalog.inventory, manifest.Inventory
	if got.Files != want.Files || got.UpstreamRules != want.UpstreamRules || got.ImportedRules != want.ImportedRules ||
		!maps.Equal(got.Skipped, want.Skipped) || !maps.Equal(got.ExcludedRules, want.ExcludedRules) || catalog.digest != manifest.RulesDigest {
		return fmt.Errorf(
			"secretmatch: kingfisher inventory drift: got files=%d upstream=%d imported=%d skipped=%v excluded=%v digest=%s; want files=%d upstream=%d imported=%d skipped=%v excluded=%v digest=%s",
			got.Files, got.UpstreamRules, got.ImportedRules, got.Skipped, got.ExcludedRules, catalog.digest,
			want.Files, want.UpstreamRules, want.ImportedRules, want.Skipped, want.ExcludedRules, manifest.RulesDigest,
		)
	}
	return nil
}

func compileKingfisherRule(source *kingfisherSourceRule) (kingfisherRule, string, error) {
	if strings.TrimSpace(source.ID) == "" || !strings.HasPrefix(source.ID, "kingfisher.") || strings.TrimSpace(source.Name) == "" {
		return kingfisherRule{}, "", errors.New("id with kingfisher namespace and name are required")
	}
	if source.Visible != nil && !*source.Visible {
		return kingfisherRule{}, "hidden", nil
	}
	confidence := strings.ToLower(strings.TrimSpace(source.Confidence))
	if confidence == "" {
		confidence = "medium"
	}
	if confidence == "low" {
		return kingfisherRule{}, "low_confidence", nil
	}
	if confidence != "medium" && confidence != "high" {
		return kingfisherRule{}, "", fmt.Errorf("unsupported confidence %q", source.Confidence)
	}
	if source.PatternRequirements.Checksum != nil && source.PatternRequirements.Checksum.Kind != 0 {
		return kingfisherRule{}, "checksum", nil
	}
	pattern, err := transpileKingfisherRegex(source.Pattern)
	if err != nil {
		return kingfisherRule{}, "incompatible_regex", nil //nolint:nilerr // Unsupported syntax skips this rule.
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return kingfisherRule{}, "incompatible_regex", nil //nolint:nilerr // Unsupported syntax skips this rule.
	}
	if re.NumSubexp() == 0 {
		return kingfisherRule{}, "no_secret_capture", nil
	}
	rule := kingfisherRule{
		id: source.ID, title: strings.TrimSpace(source.Name), regex: re,
		keywords: requiredRegexKeywords(pattern), entropy: source.MinEntropy,
		requirements: source.PatternRequirements,
	}
	if err := validateKingfisherExamples(rule, source.Examples, source.NegativeExamples); err != nil {
		return kingfisherRule{}, "example_mismatch", nil //nolint:nilerr // Invalid examples skip this rule.
	}
	return rule, "", nil
}

func (r kingfisherRule) gitleaksRule() (gconfig.Rule, error) {
	gr := gconfig.Rule{
		RuleID: r.id, Description: r.title, Regex: r.regex, Keywords: r.keywords,
		SecretGroup: 1, Entropy: r.entropy, Tags: []string{"vendor:kingfisher"},
	}
	if err := gr.Validate(); err != nil {
		return gconfig.Rule{}, err
	}
	return gr, nil
}

func validateKingfisherExamples(rule kingfisherRule, examples, negativeExamples []string) error {
	for _, example := range examples {
		match := rule.regex.FindStringSubmatch(example)
		if len(match) < 2 || !rule.requirements.accept(match[1]) {
			return errors.New("positive example is not preserved")
		}
	}
	for _, example := range negativeExamples {
		match := rule.regex.FindStringSubmatch(example)
		if len(match) >= 2 && rule.requirements.accept(match[1]) {
			return errors.New("negative example still matches")
		}
	}
	return nil
}

func (r kingfisherRequirements) empty() bool {
	return r.MinDigits == nil && r.MinUppercase == nil && r.MinLowercase == nil && r.MinSpecialChars == nil &&
		len(r.IgnoreIfContains) == 0
}

func (r kingfisherRequirements) accept(secret string) bool {
	var digits, uppercase, lowercase int
	for _, ch := range secret {
		switch {
		case ch >= '0' && ch <= '9':
			digits++
		case ch >= 'A' && ch <= 'Z':
			uppercase++
		case ch >= 'a' && ch <= 'z':
			lowercase++
		}
	}
	if below(digits, r.MinDigits) || below(uppercase, r.MinUppercase) || below(lowercase, r.MinLowercase) {
		return false
	}
	if r.MinSpecialChars != nil {
		specials := r.SpecialChars
		if specials == "" {
			specials = defaultSpecials
		}
		count := 0
		for _, ch := range secret {
			if strings.ContainsRune(specials, ch) {
				count++
			}
		}
		if count < *r.MinSpecialChars {
			return false
		}
	}
	lowerSecret := strings.ToLower(secret)
	for _, term := range r.IgnoreIfContains {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" && strings.Contains(lowerSecret, term) {
			return false
		}
	}
	return true
}

func below(got int, minimum *int) bool { return minimum != nil && got < *minimum }

func transpileKingfisherRegex(pattern string) (string, error) {
	pattern = strings.TrimSpace(stripRegexComments(pattern))
	flags := ""
	extended := false
	for strings.HasPrefix(pattern, "(?") {
		end := strings.IndexByte(pattern, ')')
		if end < 0 {
			break
		}
		group := pattern[2:end]
		if group == "" || strings.ContainsAny(group, ":<P=!#") {
			break
		}
		valid := true
		for _, flag := range group {
			if !strings.ContainsRune("imsUx-", flag) {
				valid = false
				break
			}
			if flag == 'x' {
				extended = true
			} else if flag != '-' && !strings.ContainsRune(flags, flag) {
				flags += string(flag)
			}
		}
		if !valid {
			break
		}
		pattern = strings.TrimSpace(pattern[end+1:])
	}
	if extended {
		pattern = compactExtendedRegex(pattern)
	}
	if flags != "" {
		pattern = "(?" + flags + ")" + pattern
	}
	if pattern == "" {
		return "", errors.New("empty regex")
	}
	return pattern, nil
}

func stripRegexComments(pattern string) string {
	for {
		start := strings.Index(pattern, "(?#")
		if start < 0 {
			return pattern
		}
		end := strings.IndexByte(pattern[start+3:], ')')
		if end < 0 {
			return pattern
		}
		pattern = pattern[:start] + pattern[start+3+end+1:]
	}
}

func compactExtendedRegex(pattern string) string {
	var out strings.Builder
	inClass, escaped, comment := false, false, false
	for _, ch := range pattern {
		if comment {
			if ch == '\n' || ch == '\r' {
				comment = false
			}
			continue
		}
		if escaped {
			out.WriteRune(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			out.WriteRune(ch)
			escaped = true
			continue
		}
		if inClass {
			out.WriteRune(ch)
			if ch == ']' {
				inClass = false
			}
			continue
		}
		switch ch {
		case '[':
			inClass = true
			out.WriteRune(ch)
		case '#':
			comment = true
		case ' ', '\t', '\n', '\r':
		default:
			out.WriteRune(ch)
		}
	}
	return out.String()
}

func requiredRegexKeywords(pattern string) []string {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	set := guaranteedLiterals(re)
	out := make([]string, 0, len(set))
	for literal := range set {
		if utf8.RuneCountInString(literal) >= 3 {
			out = append(out, strings.ToLower(literal))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func guaranteedLiterals(re *syntax.Regexp) map[string]struct{} {
	switch re.Op {
	case syntax.OpLiteral:
		return map[string]struct{}{string(re.Rune): {}}
	case syntax.OpCapture:
		return guaranteedLiterals(re.Sub[0])
	case syntax.OpConcat:
		out := map[string]struct{}{}
		for _, sub := range re.Sub {
			for literal := range guaranteedLiterals(sub) {
				out[literal] = struct{}{}
			}
		}
		return out
	case syntax.OpAlternate:
		if len(re.Sub) == 0 {
			return nil
		}
		out := guaranteedLiterals(re.Sub[0])
		for _, sub := range re.Sub[1:] {
			next := guaranteedLiterals(sub)
			for literal := range out {
				if _, ok := next[literal]; !ok {
					delete(out, literal)
				}
			}
		}
		return out
	case syntax.OpPlus:
		return guaranteedLiterals(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min > 0 {
			return guaranteedLiterals(re.Sub[0])
		}
	default:
	}
	return nil
}
