package structrewrite

import (
	"context"
	"strings"
)

// BareBinaryMetavarOp identifies binary patterns with two unconstrained operands.
// Such patterns match every use of the operator, including type unions.
func BareBinaryMetavarOp(ctx context.Context, langName, filename, pattern string) (op string, ok bool) {
	lang, canonical, err := resolveLanguage(langName, filename)
	if err != nil {
		return "", false
	}
	cp, err := compilePattern(ctx, lang, canonical, pattern)
	if err != nil || cp == nil || cp.root == nil {
		return "", false
	}
	return bareBinaryMetavarOp(cp.root)
}

func bareBinaryMetavarOp(root *patternNode) (op string, ok bool) {
	if root == nil || root.meta != nil || root.terminal {
		return "", false
	}
	metas := 0
	var opParts []string
	other := 0
	for _, c := range root.children {
		if c == nil {
			continue
		}
		if c.meta != nil {
			if c.meta.ellipsis {
				return "", false
			}
			metas++
			continue
		}
		if c.terminal && !c.isNamed {
			if t := strings.TrimSpace(c.text); t != "" {
				opParts = append(opParts, t)
			}
			continue
		}
		other++
	}
	if metas != 2 || other != 0 || len(opParts) == 0 {
		return "", false
	}
	return strings.Join(opParts, ""), true
}
