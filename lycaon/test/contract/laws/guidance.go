package laws

import (
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
)

// HintCodeShape is the spelling of a hint code.
var HintCodeShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

// GuidanceEmission is the inventory of agent-feedback emission sites in
// production Go. Sites are "file:line", module-relative.
type GuidanceEmission struct {
	// FormatCalls maps a hint code to its .Format and FormatCoordinatorNudge sites.
	FormatCalls map[string][]string
	// HintCodeRefs maps a registered hint code to string literals naming it
	// in internal/.
	HintCodeRefs map[string][]string
	// QueuePendingTextSites are calls of QueuePendingText.
	QueuePendingTextSites []string
	// InlineCodeNudgeLiterals are "Code: FOO — …" literals in coordinator packages.
	InlineCodeNudgeLiterals []string
}

// HasEmissionSite reports whether production code formats or names code.
func (s *GuidanceEmission) HasEmissionSite(code string) bool {
	return len(s.FormatCalls[code]) > 0 || len(s.HintCodeRefs[code]) > 0
}

// GuidanceEmission returns the emission inventory, built once.
func (c *Corpus) GuidanceEmission() *GuidanceEmission {
	g := c.Go()
	return part(c, "guidance emission", func() (*GuidanceEmission, error) {
		cfg, err := guidance.LoadHintConfigStock()
		if err != nil {
			return nil, err
		}
		if len(cfg.HintCodes) == 0 {
			return nil, fmt.Errorf("the hint registry is empty")
		}
		out := &GuidanceEmission{FormatCalls: map[string][]string{}, HintCodeRefs: map[string][]string{}}
		for _, f := range g.files {
			if f.IsTest {
				continue
			}
			site := func(pos token.Pos) string { return fmt.Sprintf("%s:%d", f.Rel, g.Line(pos)) }
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					if code, ok := formatCallHintCode(node); ok {
						out.FormatCalls[code] = appendUnique(out.FormatCalls[code], site(node.Pos()))
					}
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

func formatCallHintCode(call *ast.CallExpr) (string, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		switch fn.Sel.Name {
		case "FormatCoordinatorNudge":
		case "Format":
			if !isRejectFormatter(fn.X) {
				return "", false
			}
		default:
			return "", false
		}
	case *ast.Ident:
		if fn.Name != "FormatCoordinatorNudge" {
			return "", false
		}
	default:
		return "", false
	}
	if len(call.Args) == 0 {
		return "", false
	}
	code, _ := StringLit(call.Args[0])
	return code, HintCodeShape.MatchString(code)
}

func isRejectFormatter(x ast.Expr) bool {
	switch e := x.(type) {
	case *ast.Ident:
		return slices.Contains([]string{"f", "rejectFmt", "RejectFmt", "formatter", "RejectFormatter"}, e.Name)
	case *ast.SelectorExpr:
		return e.Sel.Name == "RejectFmt" || e.Sel.Name == "rejectFmt"
	}
	return false
}

func appendUnique(items []string, item string) []string {
	if slices.Contains(items, item) {
		return items
	}
	return append(items, item)
}
