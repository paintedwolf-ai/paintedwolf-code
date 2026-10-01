package designkit

import (
	"fmt"
	"sort"
	"strings"
)

var iconByName = func() map[string]Icon {
	m := make(map[string]Icon, len(icons))
	for _, ic := range icons {
		m[ic.Name] = ic
	}
	return m
}()

// Icons returns a copy of the bundled icon catalog.
func Icons() []Icon {
	return append([]Icon(nil), icons...)
}

// IconNames returns sorted canonical pack entry names.
func IconNames() []string {
	out := make([]string, 0, len(icons))
	for _, ic := range icons {
		out = append(out, ic.Name)
	}
	sort.Strings(out)
	return out
}

func iconSearchTerm(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	return strings.ReplaceAll(name, " ", "-")
}

// SuggestIcons returns matching canonical catalog names.
func SuggestIcons(guess string, n int) []string {
	guess = iconSearchTerm(guess)
	if guess == "" || n <= 0 {
		return nil
	}
	var prefix, contain []string
	for _, name := range IconNames() {
		if strings.HasPrefix(name, guess) {
			prefix = append(prefix, name)
		} else if strings.Contains(name, guess) {
			contain = append(contain, name)
		}
	}
	out := append(append([]string(nil), prefix...), contain...)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// LookupIcon resolves a canonical catalog name.
func LookupIcon(name string) (Icon, bool) {
	ic, ok := iconByName[name]
	if !ok {
		return Icon{}, false
	}
	return ic, true
}

// IconSpriteHTML returns a hidden SVG symbol sprite for <use href="#kit-NAME"/>.
// When only is non-empty, only named catalog icons are emitted.
func IconSpriteHTML(only ...string) string {
	var list []Icon
	if len(only) == 0 {
		list = append([]Icon(nil), icons...)
	} else {
		seen := map[string]bool{}
		for _, name := range only {
			ic, ok := LookupIcon(name)
			if !ok || seen[ic.Name] {
				continue
			}
			seen[ic.Name] = true
			list = append(list, ic)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	}
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="0" height="0" style="position:absolute;overflow:hidden" aria-hidden="true"><defs>`)
	for _, ic := range list {
		fmt.Fprintf(&b, `<symbol id="kit-%s" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">%s</symbol>`,
			ic.Name, ic.Inner)
	}
	b.WriteString(`</defs></svg>`)
	return b.String()
}

// IconsReferencedInMarkup returns kit icon names referenced via href="#kit-…" / xlink.
func IconsReferencedInMarkup(markup string) []string {
	remaining := markup
	var out []string
	seen := map[string]bool{}
	for {
		i := strings.Index(remaining, "#kit-")
		if i < 0 {
			break
		}
		rest := remaining[i+5:]
		end := 0
		for end < len(rest) {
			c := rest[end]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
				end++
				continue
			}
			break
		}
		if end > 0 {
			name := rest[:end]
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		remaining = rest
	}
	sort.Strings(out)
	return out
}

// ValidateIconRefs rejects unknown #kit-* names with suggestions.
func ValidateIconRefs(markup string) error {
	var unknown []string
	suggestions := map[string][]string{}
	for _, name := range IconsReferencedInMarkup(markup) {
		if _, ok := iconByName[name]; ok {
			continue
		}
		unknown = append(unknown, name)
		suggestions[name] = SuggestIcons(name, 5)
	}
	if len(unknown) == 0 {
		return nil
	}
	return &IconRefError{Unknown: unknown, Suggestions: suggestions}
}

// IconRefError lists unknown kit icon tokens.
type IconRefError struct {
	Unknown     []string
	Suggestions map[string][]string
}

func (e *IconRefError) Error() string {
	return fmt.Sprintf("unknown kit icons: %s", strings.Join(e.Unknown, ", "))
}
