package structrewrite

import "strings"

// renderFix substitutes captured text without parsing the replacement.
// Unknown placeholders remain literal.
func renderFix(fix string, m Match) string {
	if !strings.ContainsRune(fix, '$') {
		return fix
	}
	var b strings.Builder
	b.Grow(len(fix))
	rs := []rune(fix)
	for i := 0; i < len(rs); {
		if rs[i] != '$' {
			b.WriteRune(rs[i])
			i++
			continue
		}
		token, name, n := scanPlaceholder(rs, i)
		if name == "" {
			b.WriteString(token) // not a placeholder; emit sigils literally
			i += n
			continue
		}
		if val, ok := m.Bindings[name]; ok {
			b.WriteString(val)
		} else {
			b.WriteString(token) // unbound — leave as written
		}
		i += n
	}
	return b.String()
}

// scanPlaceholder reads a `$NAME` / `$$$NAME` token starting at rs[i]. It
// returns the literal token text, the extracted metavariable name (empty if the
// run is not a valid placeholder), and the number of runes consumed.
func scanPlaceholder(rs []rune, i int) (token, name string, consumed int) {
	j := i
	for j < len(rs) && rs[j] == '$' {
		j++
	}
	dollars := j - i
	k := j
	for k < len(rs) {
		r := rs[k]
		if r == '_' || (r >= 'A' && r <= 'Z') || (k > j && r >= '0' && r <= '9') {
			k++
			continue
		}
		break
	}
	literal := string(rs[i:k])
	ident := string(rs[j:k])
	if (dollars == 1 || dollars == 3) && ident != "" && isMetaName(ident) {
		return literal, ident, k - i
	}
	// Not a placeholder: consume only the sigil run so following text is rescanned.
	return string(rs[i:j]), "", dollars
}
