package laws

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
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
