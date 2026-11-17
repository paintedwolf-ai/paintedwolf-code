package laws

import (
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"gopkg.in/yaml.v3"
)

// GuidanceEmission is the inventory of agent-feedback emission sites in
// production Go. Sites are "file:line", module-relative.
type GuidanceEmission struct {
	// HintCodeRefs maps a registered hint code to string literals naming it
	// in internal/.
	HintCodeRefs map[string][]string
	// QueuePendingTextSites are calls of QueuePendingText.
	QueuePendingTextSites []string
	// InlineCodeNudgeLiterals are "Code: FOO — …" literals in coordinator packages.
	InlineCodeNudgeLiterals []string
}

// HasEmissionSite reports whether production code names code.
func (s *GuidanceEmission) HasEmissionSite(code string) bool {
	return len(s.HintCodeRefs[code]) > 0
}

// GuidanceEmission returns the emission inventory, built once.
func (c *Corpus) GuidanceEmission() *GuidanceEmission {
	g := c.Go()
	cfg := HintConfig(c)
	return part(c, "guidance emission", func() (*GuidanceEmission, error) {
		out := &GuidanceEmission{HintCodeRefs: map[string][]string{}}
		for _, f := range g.files {
			if f.IsTest {
				continue
			}
			site := func(pos token.Pos) string { return fmt.Sprintf("%s:%d", f.Rel, g.Line(pos)) }
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					if CalleeName(node) == "QueuePendingText" {
						out.QueuePendingTextSites = append(out.QueuePendingTextSites, site(node.Pos()))
					}
				case *ast.BasicLit:
					value, ok := StringLit(node)
					if !ok {
						return true
					}
					if Under(f, "internal/session", "internal/delegation", "internal/coordinator") &&
						strings.Contains(value, "Code:") && (strings.Contains(value, "—") || strings.Contains(value, " - ")) {
						out.InlineCodeNudgeLiterals = append(out.InlineCodeNudgeLiterals, site(node.Pos()))
					}
					if _, registered := cfg.HintCodes[value]; registered && Under(f, "internal") {
						out.HintCodeRefs[value] = appendUnique(out.HintCodeRefs[value], site(node.Pos()))
					}
				}
				return true
			})
		}
		return out, nil
	})
}

func appendUnique(items []string, item string) []string {
	if slices.Contains(items, item) {
		return items
	}
	return append(items, item)
}

// HintConfig returns the stock hint registry, loaded once.
func HintConfig(c *Corpus) *guidance.HintConfig {
	return part(c, "hint registry", func() (*guidance.HintConfig, error) {
		cfg, err := guidance.LoadHintConfigStock()
		if err == nil && len(cfg.HintCodes) == 0 {
			err = fmt.Errorf("the hint registry is empty")
		}
		return cfg, err
	})
}

var catalogCodeRE = regexp.MustCompile(`\bcode:\s*([A-Z][A-Z0-9_]+)\b`)

// CatalogHintCodes returns the hint codes catalog YAML wires through
// `code:` keys: posture rules and agent tool profiles.
func CatalogHintCodes(c *Corpus) map[string]bool {
	return part(c, "catalog hint codes", func() (map[string]bool, error) {
		out := map[string]bool{}
		add := func(text string) {
			for _, m := range catalogCodeRE.FindAllStringSubmatch(text, -1) {
				out[m[1]] = true
			}
		}
		const host = "lycaon/config/packs/painted-wolf/platform/host/"
		files := c.Tree(host+"posture-rules", ".yaml")
		for _, f := range files {
			add(f.Text())
		}
		add(c.Text(host + "agent-tool-profiles.yaml"))
		return out, nil
	})
}

// OARAnchoredCodes returns hint codes the block plane emits directly: the
// registry entries carrying an `on` anchor.
func OARAnchoredCodes(c *Corpus) map[string]bool {
	return part(c, "OAR anchored codes", func() (map[string]bool, error) {
		entries, err := hintregistry.ListEffective()
		if err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for _, ent := range entries {
			var probe map[string]any
			if err := yaml.Unmarshal(ent.Body, &probe); err != nil {
				return nil, fmt.Errorf("hint %s: %w", ent.Code, err)
			}
			if anchor, ok := probe["on"].(string); ok && strings.TrimSpace(anchor) != "" {
				out[ent.Code] = true
				continue
			}
			if hc, ok := probe["hint_codes"].(map[string]any); ok {
				if body, ok := hc[ent.Code].(map[string]any); ok {
					if anchor, ok := body["on"].(string); ok && strings.TrimSpace(anchor) != "" {
						out[ent.Code] = true
					}
				}
			}
		}
		return out, nil
	})
}
