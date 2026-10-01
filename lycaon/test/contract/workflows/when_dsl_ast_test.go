package contract

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/rules"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var forbiddenWhenIdents = []*regexp.Regexp{
	regexp.MustCompile(`^mode_is`),
	regexp.MustCompile(`^mode_unresolved`),
	regexp.MustCompile(`^tool_is_den`),
	regexp.MustCompile(`^tool_is_rally`),
	regexp.MustCompile(`^tool_is_call`),
	regexp.MustCompile(`^stage_[a-z0-9_]+_complete$`),
	regexp.MustCompile(`^user_input_`),
}

func TestHintWhenExpressionsValidate(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	contractcheck.FailErr(t, "install catalog", anchorcatalog.InstallFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))
	schemaDir := filepath.Join(root, "schemas")
	hintsAt := extpacks.Bundled(hintregistry.DefaultDir)
	loader, err := oar.NewLoader(schemaDir)
	contractcheck.FailErr(t, "oar loader", err)
	rs, err := loader.LoadDir(hintsAt)
	contractcheck.FailErr(t, "load OAR rules", err)
	cfg, err := guidance.LoadHintConfig(hintsAt)
	contractcheck.FailErr(t, "load hint registry", err)
	for code, entry := range cfg.HintCodes {
		when := strings.TrimSpace(entry.When)
		if when == "" {
			continue
		}
		r, ok := rs.Get(code)
		if !ok {
			t.Errorf("hint %q has when but is not an OAR rule", code)
			continue
		}
		if r.When != when {
			t.Errorf("hint %q: loaded when %q, want %q", code, r.When, when)
		}
		for _, re := range forbiddenWhenIdents {
			if re.MatchString(when) {
				t.Errorf("hint %q: forbidden pattern in when %q", code, when)
			}
		}
	}
}

func TestBundledRulesCanonicalWhenMatchesRuleWhenKeys(t *testing.T) {
	allowed := knownRuleWhenKeys()
	reg := ruleConditionRegistry(t)
	ents, err := config.List(config.PostureRulesDir)
	contractcheck.FailErr(t, "list posture-rules", err)
	for _, ent := range ents {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		cfg, err := rules.LoadRulesConfig(config.PostureRulesDir.Join(name))
		contractcheck.FailErr(t, "load rules config YAML", err)
		for _, rule := range cfg.Rules {
			canon, err := rules.CanonicalWhenString(rule.When)
			if err != nil {
				t.Errorf("%s rule %q: %v", name, rule.ID, err)
				continue
			}
			tree, err := boolexpr.Parse(canon)
			if err != nil {
				t.Errorf("%s rule %q: parse canonical %q: %v", name, rule.ID, canon, err)
				continue
			}
			if err := boolexpr.ValidateWhitelist(tree); err != nil {
				t.Errorf("%s rule %q: whitelist: %v", name, rule.ID, err)
			}
			for _, id := range boolexpr.CollectIdents(tree) {
				// posture_is carries its value in map form.
				if strings.HasPrefix(id, "posture_is_") {
					continue
				}
				key := id
				_, fixed := allowed[key]
				if !fixed && !reg.Has(key) {
					t.Errorf("%s rule %q: canonical %q uses unknown key %q", name, rule.ID, canon, id)
				}
			}
		}
	}
}
