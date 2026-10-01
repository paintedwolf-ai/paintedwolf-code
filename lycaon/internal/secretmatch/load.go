package secretmatch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"

	gconfig "github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
	"gopkg.in/yaml.v3"
)

var ruleIDStem = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ruleFile is one secret-pattern catalog unit: either upstream tuning or a
// local regex rule.
type ruleFile struct {
	ID                 string   `yaml:"id"`
	UpstreamID         string   `yaml:"upstream_id"`
	Title              string   `yaml:"title"`
	Regex              string   `yaml:"regex"`
	Keywords           []string `yaml:"keywords"`
	Severity           string   `yaml:"severity"`
	SecretGroup        int      `yaml:"secret_group"`
	Entropy            float64  `yaml:"entropy"`
	AllowlistStopWords []string `yaml:"allowlist_stopwords"`
	Enabled            *bool    `yaml:"enabled"`
}

func validRuleID(id string) bool {
	return ruleIDStem.MatchString(strings.TrimSpace(id))
}

func parseRuleFile(data []byte) (ruleFile, error) {
	var rf ruleFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&rf); err != nil {
		return ruleFile{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return ruleFile{}, err
		}
		return ruleFile{}, errors.New("multiple YAML documents are not supported")
	}
	return rf, nil
}

type configProfile uint8

const (
	outboundProfile configProfile = iota
	scannerProfile
)

// BuildMatcher compiles the merged outbound catalog.
func BuildMatcher(layers ...Layer) (*Matcher, error) {
	cfg, units, err := buildConfig(outboundProfile, layers...)
	if err != nil {
		return nil, err
	}

	vendor, err := bundledKingfisherCatalog()
	if err != nil {
		return nil, err
	}
	placeholders, err := bundledPlaceholders()
	if err != nil {
		return nil, err
	}
	m := matcherFromUnits(units)
	m.addKingfisher(vendor)
	m.placeholders = placeholders
	built := detect.NewDetector(cfg)
	built.MaxDecodeDepth = 2
	m.detector = built
	return m, nil
}

// ScannerProfile couples repository rules with offline constraints.
type ScannerProfile struct {
	config       gconfig.Config
	requirements map[string]kingfisherRequirements
}

// BuildScannerProfile constructs the complete at-rest profile.
func BuildScannerProfile(layers ...Layer) (*ScannerProfile, error) {
	cfg, units, err := buildConfig(scannerProfile, layers...)
	if err != nil {
		return nil, err
	}
	vendor, err := bundledKingfisherCatalog()
	if err != nil {
		return nil, err
	}
	if len(units) == 0 && len(vendor.rules) == 0 {
		return nil, errors.New("secretmatch: scanner catalog has no units")
	}
	return &ScannerProfile{config: cfg, requirements: vendor.requirements}, nil
}

// Config returns the compiled scanner configuration.
func (p *ScannerProfile) Config() gconfig.Config {
	if p == nil {
		return gconfig.Config{}
	}
	return p.config
}

// Accept reports whether a transient finding satisfies its vendor rule's
// offline constraints. Non-vendor rules have no additional constraint.
func (p *ScannerProfile) Accept(ruleID, secret string) bool {
	if p == nil {
		return false
	}
	requirement, ok := p.requirements[strings.TrimPrefix(ruleID, gitleaksPrefix)]
	return !ok || requirement.accept(secret)
}

// PreferFinding reports whether candidate has better catalog attribution than
// current when both findings describe the same secret span.
func (p *ScannerProfile) PreferFinding(candidate, current string) bool {
	return findingRank(candidate) > findingRank(current)
}

func findingRank(ruleID string) int {
	ruleID = strings.TrimPrefix(ruleID, gitleaksPrefix)
	if strings.HasPrefix(ruleID, "kingfisher.") {
		return 0
	}
	return 1
}

// Default configuration parsing mutates process-wide state.
var (
	defaultConfigOnce sync.Once
	defaultConfig     gconfig.Config
	defaultConfigErr  error
)

func gitleaksDefaultConfig() (gconfig.Config, error) {
	defaultConfigOnce.Do(func() {
		detector, err := detect.NewDetectorDefaultConfig()
		if err != nil {
			defaultConfigErr = fmt.Errorf("secretmatch: gitleaks detector: %w", err)
			return
		}
		defaultConfig = detector.Config
	})
	return defaultConfig, defaultConfigErr
}

func buildConfig(profile configProfile, layers ...Layer) (gconfig.Config, []catalogUnit, error) {
	base, err := gitleaksDefaultConfig()
	if err != nil {
		return gconfig.Config{}, nil, err
	}

	units, err := loadCatalogUnits(layers...)
	if err != nil {
		return gconfig.Config{}, nil, err
	}

	// Copy mutable fields before profile-specific filtering.
	cfg := base
	cfg.Rules = make(map[string]gconfig.Rule, len(base.Rules))
	for id, rule := range base.Rules {
		cfg.Rules[id] = rule
	}
	cfg.OrderedRules = append([]string(nil), base.OrderedRules...)
	for _, u := range units {
		if u.UpstreamID != "" {
			rule, ok := cfg.Rules[u.UpstreamID]
			if !ok {
				return gconfig.Config{}, nil, fmt.Errorf("secretmatch: %s: upstream_id %q is not in the default rule set", u.Path, u.UpstreamID)
			}
			if err := appendStopwordAllowlist(&rule, u); err != nil {
				return gconfig.Config{}, nil, err
			}
			cfg.Rules[u.UpstreamID] = rule
			if profile == outboundProfile && !u.enabled() {
				delete(cfg.Rules, u.UpstreamID)
			}
			continue
		}

		// Local rule.
		if _, collides := cfg.Rules[u.ID]; collides {
			return gconfig.Config{}, nil, fmt.Errorf(
				"secretmatch: %s: local rule id %q collides with an upstream rule; use upstream_id for tuning",
				u.Path,
				u.ID,
			)
		}
		re, err := regexp.Compile(u.Regex)
		if err != nil {
			return gconfig.Config{}, nil, fmt.Errorf("secretmatch: %s: bad regex: %w", u.Path, err)
		}
		gr := gconfig.Rule{
			RuleID:      u.ID,
			Description: u.Title,
			Regex:       re,
			Keywords:    u.Keywords,
			SecretGroup: u.SecretGroup,
			Entropy:     u.Entropy,
		}
		if len(u.AllowlistStopWords) > 0 {
			gr.Allowlists = []*gconfig.Allowlist{{StopWords: append([]string(nil), u.AllowlistStopWords...)}}
		}
		if err := gr.Validate(); err != nil {
			return gconfig.Config{}, nil, fmt.Errorf("secretmatch: %s: local rule: %w", u.Path, err)
		}
		if profile == outboundProfile && !u.enabled() {
			continue
		}
		cfg.Rules[u.ID] = gr
		cfg.OrderedRules = append(cfg.OrderedRules, u.ID)
	}

	vendor, err := bundledKingfisherCatalog()
	if err != nil {
		return gconfig.Config{}, nil, err
	}
	for _, rule := range vendor.rules {
		if profile == outboundProfile {
			if _, excluded := vendor.outboundExclusions[rule.id]; excluded {
				continue
			}
		}
		if _, collides := cfg.Rules[rule.id]; collides {
			return gconfig.Config{}, nil, fmt.Errorf("secretmatch: kingfisher rule id %q collides with an active rule", rule.id)
		}
		compiled, err := rule.gitleaksRule()
		if err != nil {
			return gconfig.Config{}, nil, fmt.Errorf("secretmatch: kingfisher rule %q: %w", rule.id, err)
		}
		cfg.Rules[rule.id] = compiled
		cfg.OrderedRules = append(cfg.OrderedRules, rule.id)
	}

	rebuildConfigIndexes(&cfg)
	return cfg, units, nil
}

func appendStopwordAllowlist(rule *gconfig.Rule, u catalogUnit) error {
	if len(u.AllowlistStopWords) == 0 {
		return nil
	}
	allow := &gconfig.Allowlist{StopWords: append([]string(nil), u.AllowlistStopWords...)}
	if err := allow.Validate(); err != nil {
		return fmt.Errorf("secretmatch: %s: allowlist_stopwords: %w", u.Path, err)
	}
	rule.Allowlists = append(rule.Allowlists, allow)
	return nil
}

func rebuildConfigIndexes(cfg *gconfig.Config) {
	cfg.Keywords = map[string]struct{}{}
	for _, rule := range cfg.Rules {
		for _, keyword := range rule.Keywords {
			cfg.Keywords[strings.ToLower(keyword)] = struct{}{}
		}
	}
	ordered := cfg.OrderedRules[:0]
	seen := make(map[string]struct{}, len(cfg.OrderedRules))
	for _, id := range cfg.OrderedRules {
		if _, active := cfg.Rules[id]; !active {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	cfg.OrderedRules = ordered
}

func matcherFromUnits(units []catalogUnit) *Matcher {
	m := &Matcher{
		titles:      map[string]string{},
		severities:  map[string]string{},
		localIDs:    map[string]struct{}{},
		vendorIDs:   map[string]struct{}{},
		screenCache: newScreenCache(),
	}
	for _, u := range units {
		if !u.enabled() {
			continue
		}
		key := u.UpstreamID
		if key == "" {
			key = u.ID
			m.localIDs[key] = struct{}{}
		}
		if u.Title != "" {
			m.titles[key] = u.Title
		}
		if u.Severity != "" {
			m.severities[key] = u.Severity
		}
	}
	return m
}

func (m *Matcher) addKingfisher(catalog kingfisherCatalog) {
	if m == nil {
		return
	}
	if m.requirements == nil {
		m.requirements = make(map[string]kingfisherRequirements, len(catalog.requirements))
	}
	for _, rule := range catalog.rules {
		_, outboundExcluded := catalog.outboundExclusions[rule.id]
		if outboundExcluded {
			continue
		}
		m.localIDs[rule.id] = struct{}{}
		m.vendorIDs[rule.id] = struct{}{}
		m.titles[rule.id] = rule.title
		m.severities[rule.id] = "critical"
	}
	for id, requirement := range catalog.requirements {
		m.requirements[id] = requirement
	}
	m.catalogVersion = catalog.version
}

type catalogUnit struct {
	Path               string
	ID                 string
	UpstreamID         string
	Title              string
	Regex              string
	Keywords           []string
	Severity           string
	SecretGroup        int
	Entropy            float64
	AllowlistStopWords []string
	Enabled            *bool
}

func (u catalogUnit) enabled() bool {
	return u.Enabled == nil || *u.Enabled
}

func loadCatalogUnits(layers ...Layer) ([]catalogUnit, error) {
	byID := map[string]catalogUnit{}
	order := make([]string, 0)
	for _, layer := range layers {
		ents, err := layer.list()
		if err != nil {
			return nil, err
		}
		for _, name := range ents {
			raw, path, err := layer.read(name)
			if err != nil {
				return nil, err
			}
			u, err := compileCatalogUnit(path, name, raw)
			if err != nil {
				return nil, err
			}
			if _, ok := byID[u.ID]; !ok {
				order = append(order, u.ID)
			}
			byID[u.ID] = u
		}
	}
	sort.Strings(order)
	out := make([]catalogUnit, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

func compileCatalogUnit(path, filename string, raw []byte) (catalogUnit, error) {
	rf, err := parseRuleFile(raw)
	if err != nil {
		return catalogUnit{}, fmt.Errorf("secretmatch: parse %s: %w", path, err)
	}
	stem := strings.TrimSuffix(strings.TrimSuffix(filename, ".yaml"), ".yml")
	id := strings.TrimSpace(rf.ID)
	if id == "" {
		return catalogUnit{}, fmt.Errorf("secretmatch: %s: missing id", path)
	}
	if !validRuleID(id) {
		return catalogUnit{}, fmt.Errorf("secretmatch: %s: invalid id %q", path, id)
	}
	if id != stem {
		return catalogUnit{}, fmt.Errorf("secretmatch: %s: id %q must match filename stem %q", path, id, stem)
	}

	upstream := strings.TrimSpace(rf.UpstreamID)
	pat := strings.TrimSpace(rf.Regex)
	if upstream != "" && pat != "" {
		return catalogUnit{}, fmt.Errorf("secretmatch: %s: cannot set both upstream_id and regex", path)
	}
	if upstream == "" && pat == "" {
		return catalogUnit{}, fmt.Errorf("secretmatch: %s: need upstream_id (tuning) or regex (local)", path)
	}
	if upstream != "" && id != upstream {
		return catalogUnit{}, fmt.Errorf(
			"secretmatch: %s: tuning id %q must match upstream_id %q",
			path,
			id,
			upstream,
		)
	}

	sev := strings.TrimSpace(strings.ToLower(rf.Severity))
	if sev != "" && sev != "high" && sev != "critical" {
		return catalogUnit{}, fmt.Errorf("secretmatch: %s: severity %q must be high or critical", path, rf.Severity)
	}

	title := strings.TrimSpace(rf.Title)
	if upstream == "" {
		// Local rule requires title + severity.
		if title == "" {
			return catalogUnit{}, fmt.Errorf("secretmatch: %s: missing title", path)
		}
		if sev == "" {
			return catalogUnit{}, fmt.Errorf("secretmatch: %s: missing severity", path)
		}
	}

	kws := make([]string, 0, len(rf.Keywords))
	for _, kw := range rf.Keywords {
		kw = strings.TrimSpace(kw)
		if kw != "" {
			kws = append(kws, kw)
		}
	}
	stopwords := make([]string, 0, len(rf.AllowlistStopWords))
	for _, stopword := range rf.AllowlistStopWords {
		stopword = strings.TrimSpace(stopword)
		if stopword != "" {
			stopwords = append(stopwords, stopword)
		}
	}
	return catalogUnit{
		Path:               path,
		ID:                 id,
		UpstreamID:         upstream,
		Title:              title,
		Regex:              pat,
		Keywords:           kws,
		Severity:           sev,
		SecretGroup:        rf.SecretGroup,
		Entropy:            rf.Entropy,
		AllowlistStopWords: stopwords,
		Enabled:            rf.Enabled,
	}, nil
}
