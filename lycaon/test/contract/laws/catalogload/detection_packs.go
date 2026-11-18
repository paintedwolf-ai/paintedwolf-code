package catalogload

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// detectionCatalog loads the shipped detection packs against an empty user
// configuration.
func detectionCatalog(r *laws.Run) *detectionpack.Catalog {
	return laws.Part(r.Corpus, "detection catalog", func() (*detectionpack.Catalog, error) {
		packs, warnings, err := detectionpack.ShippedPacks()
		if err != nil {
			return nil, err
		}
		if len(warnings) != 0 {
			return nil, fmt.Errorf("shipped detection warnings: %v", warnings)
		}
		cat, err := detectionpack.LoadCatalog(detectionpack.Input{ConfigDir: r.Scratch(), Contributed: packs})
		if err == nil && len(cat.Packs) == 0 {
			err = fmt.Errorf("no detection packs loaded")
		}
		return cat, err
	})
}

// ruleYAML reads one rule of a pack as raw YAML.
func ruleYAML(r *laws.Run, pack, slug string) map[string]any {
	data, err := config.Read(config.DetectionPacksDir.Join(pack, "rules", slug+".yml"))
	r.Must(err)
	var raw map[string]any
	r.Must(yaml.Unmarshal(data, &raw))
	return raw
}

// ruleImages lists the lower-cased Image values a rule's selections name.
func ruleImages(r *laws.Run, pack, slug string) []string {
	det, _ := ruleYAML(r, pack, slug)["detection"].(map[string]any)
	var out []string
	for key, value := range det {
		sel, ok := value.(map[string]any)
		if key == "condition" || !ok {
			continue
		}
		for field, v := range sel {
			if strings.Split(field, "|")[0] != "Image" {
				continue
			}
			switch x := v.(type) {
			case string:
				out = append(out, strings.ToLower(x))
			case []any:
				for _, item := range x {
					if s, ok := item.(string); ok {
						out = append(out, strings.ToLower(s))
					}
				}
			}
		}
	}
	return out
}

func eachRule(r *laws.Run, visit func(p detectionpack.Pack, rule detectionpack.Rule)) {
	for _, p := range detectionCatalog(r).Packs {
		for _, rule := range p.Rules {
			visit(p, rule)
		}
	}
}

var detectionPacks = []laws.Rule{{
	// Every shipped rule is one the matcher supports.
	Name:    "detection-pack-rules-are-supported",
	Anchors: []laws.Anchor{laws.Func("internal/detectionpack", "ShippedPacks")},
	Check: func(r *laws.Run) {
		eachRule(r, func(p detectionpack.Pack, rule detectionpack.Rule) {
			if !rule.Supported {
				r.Report("%s/%s is unsupported: %s", p.ID, rule.Slug, rule.UnsupportedReason)
			}
		})
	},
}, {
	// Provider CLI packs stay point-precise: no verbs_* axis shared by rules.
	Name:    "provider-detection-packs-stay-point-precise",
	Anchors: []laws.Anchor{laws.Dir("lycaon/config/packs/painted-wolf/security/host/detection-packs/aws-cli")},
	Check: func(r *laws.Run) {
		for _, pack := range []string{"aws-cli", "azure-cli", "gcloud-cli"} {
			ents, err := config.List(config.DetectionPacksDir.Join(pack, "rules"))
			r.Must(err)
			uses := map[string]int{}
			for _, ent := range ents {
				if !strings.HasSuffix(ent.Name(), ".yml") {
					continue
				}
				det, _ := ruleYAML(r, pack, strings.TrimSuffix(ent.Name(), ".yml"))["detection"].(map[string]any)
				for key := range det {
					if strings.HasPrefix(key, "verbs_") {
						uses[key]++
					}
				}
			}
			for sel, n := range uses {
				if n >= 2 {
					r.Report("%s: %d rules share the %s axis", pack, n, sel)
				}
			}
		}
	},
}, {
	// Authored levels are live: a credential mint is informational, and
	// anything else escalates above low.
	Name:    "detection-pack-levels-are-not-inert",
	Anchors: []laws.Anchor{laws.Func("internal/detectionpack", "EffectFromTags")},
	Check: func(r *laws.Run) {
		eachRule(r, func(p detectionpack.Pack, rule detectionpack.Rule) {
			if p.Source != detectionpack.SourceBundled && p.Source != detectionpack.SourceGenerated {
				return
			}
			mint := detectionpack.EffectFromTags(rule.Tags).MintsCredential
			low := rule.Level == detectionpack.LevelLow || rule.Level == detectionpack.LevelInformational
			if mint && rule.Level != detectionpack.LevelInformational || !mint && low {
				r.Report("%s/%s level %s is inert for its effect", p.ID, rule.Slug, rule.Level)
			}
		})
	},
}, {
	// A rule with an effect class states exactly one place the effect lands,
	// so it can raise a card.
	Name:    "detection-pack-rules-are-gate-shaped",
	Anchors: []laws.Anchor{laws.Decl("internal/detectionpack", "TagEffectExternal"), laws.Decl("internal/detectionpack", "TagEffectLocal")},
	Check: func(r *laws.Run) {
		eachRule(r, func(p detectionpack.Pack, rule detectionpack.Rule) {
			effect := detectionpack.EffectFromTags(rule.Tags)
			if effect.Tagged && !effect.MintsCredential && effect.External == effect.Local {
				r.Report("%s/%s must declare exactly one of %s and %s", p.ID, rule.Slug, detectionpack.TagEffectExternal, detectionpack.TagEffectLocal)
			}
		})
	},
}, {
	// High and critical rules are unrecoverable; lower levels are not.
	Name:    "detection-pack-level-matches-effect-class",
	Anchors: []laws.Anchor{laws.Decl("internal/detectionpack", "TagEffectUnrecoverable")},
	Check: func(r *laws.Run) {
		eachRule(r, func(p detectionpack.Pack, rule detectionpack.Rule) {
			effect := detectionpack.EffectFromTags(rule.Tags)
			if !effect.Tagged {
				return
			}
			switch rule.Level {
			case detectionpack.LevelCritical, detectionpack.LevelHigh:
				if !effect.Unrecoverable {
					r.Report("%s/%s is %s but not unrecoverable", p.ID, rule.Slug, rule.Level)
				}
			case detectionpack.LevelMedium, detectionpack.LevelLow, detectionpack.LevelInformational:
				if effect.Unrecoverable {
					r.Report("%s/%s is %s but unrecoverable; it belongs at high or critical", p.ID, rule.Slug, rule.Level)
				}
			}
		})
	},
}, {
	// Image lists name the program that acts, never a launcher or a secrets
	// wrapper in front of it.
	Name:    "detection-pack-images-name-no-launchers",
	Anchors: []laws.Anchor{laws.Func("internal/detectionpack", "LoadCatalog")},
	Check: func(r *laws.Run) {
		wrappers := []string{"npx", "bunx", "aws-vault", "sudo", "env", "doppler", "op"}
		eachRule(r, func(p detectionpack.Pack, rule detectionpack.Rule) {
			for _, img := range ruleImages(r, p.ID, rule.Slug) {
				if contains(wrappers, img) {
					r.Report("%s/%s Image lists the wrapper %s", p.ID, rule.Slug, img)
				}
			}
		})
	},
}, {
	// An Image list naming one member of an equivalence class names them all.
	Name:    "detection-pack-equivalence-classes-are-complete",
	Anchors: []laws.Anchor{laws.Decl("internal/detectionpack", "Pack.Equivalents")},
	Check: func(r *laws.Run) {
		eachRule(r, func(p detectionpack.Pack, rule detectionpack.Rule) {
			if len(p.Equivalents) == 0 {
				return
			}
			images := ruleImages(r, p.ID, rule.Slug)
			for canon, alts := range p.Equivalents {
				members := append([]string{canon}, alts...)
				named := false
				for _, m := range members {
					named = named || contains(images, m)
				}
				for _, m := range members {
					if named && !contains(images, m) {
						r.Report("%s/%s Image list names the %s class but not %s", p.ID, rule.Slug, canon, m)
					}
				}
			}
		})
	},
}, {
	// Every isolation outcome and sandbox reject code has policy copy.
	Name:    "sandbox-reject-codes-have-policy-copy",
	Anchors: []laws.Anchor{laws.Func("internal/isolation", "Outcomes")},
	Check: func(r *laws.Run) {
		codes := []string{"VERIFY_UNVERIFIABLE", "WEB_SEARCH_HOST_DENIED"}
		for _, outcome := range isolation.Outcomes() {
			codes = append(codes, outcome.Code)
		}
		for _, code := range codes {
			if _, err := catalogfixture.StockPolicyFile(code); err != nil {
				r.Report("sandbox code %s has no policy copy: %v", code, err)
			}
		}
	},
}}
