package theme

import (
	"fmt"
	"sort"
	"strings"
)

// Declaration is one decoded authored theme.
type Declaration struct {
	ID         string
	Name       string
	Appearance Appearance
	// Tokens maps base-plane token id → literal color.
	Tokens map[string]string
	// Syntax maps scope id → its authored style.
	Syntax map[string]SyntaxDeclaration
	// Logomark is the authored visibility, empty when the theme says nothing.
	Logomark LogomarkVisibility
	// Icons maps slot ids to authored SVG child geometry.
	Icons map[string]string
	// Stroke is the theme-wide treatment applied to every stroked slot.
	Stroke *StrokeDeclaration
	// Bevel scales every bevel edge; nil keeps the stock strength.
	Bevel        *BevelDeclaration
	WindowColors *WindowColorsDeclaration
	AgentColors  *AgentColorsDeclaration
}

// SyntaxDeclaration is one authored syntax scope.
type SyntaxDeclaration struct {
	Color     string
	Italic    bool
	Bold      bool
	Underline bool
}

// Compiled contains the resolved theme values.
type Compiled struct {
	ID         string
	Name       string
	Appearance Appearance
	// Tokens is the complete base plane, keyed by token id.
	Tokens map[string]Color
	// Syntax is the complete scope plane, keyed by scope id.
	Syntax map[string]SyntaxStyle
	// Brand is total: an unset visibility resolves to the default.
	Brand Brand
	// Icons contains only authored slot overrides.
	Icons map[string][]IconNode
	// IconStroke applies to every stroked slot.
	IconStroke IconStroke
	// Bevel is the resolved strength; Tokens already carries the scaled edges.
	Bevel        Bevel
	WindowColors WindowColors
	AgentColors  AgentColors
}

// Fault is one typed, author-actionable compile diagnostic.
type Fault struct {
	// Field names the offending author path (`tokens.accent`).
	Field   string
	Message string
}

func (f Fault) Error() string { return f.Field + ": " + f.Message }

// FaultsError carries every fault from one theme.
type FaultsError struct{ Faults []Fault }

func (e *FaultsError) Error() string {
	parts := make([]string, 0, len(e.Faults))
	for _, f := range e.Faults {
		parts = append(parts, f.Error())
	}
	return strings.Join(parts, "; ")
}

// Compile validates and resolves one theme declaration.
func Compile(decl Declaration) (Compiled, error) {
	var faults []Fault

	if strings.TrimSpace(decl.Name) == "" {
		faults = append(faults, Fault{Field: "name", Message: "required"})
	}
	if !ValidAppearance(decl.Appearance) {
		faults = append(faults, Fault{
			Field:   "appearance",
			Message: fmt.Sprintf("%q is not one of %v", decl.Appearance, Appearances()),
		})
	}

	icons := map[string][]IconNode{}
	for _, id := range sortedKeys(decl.Icons) {
		if _, ok := IconSlotByID(id); !ok {
			faults = append(faults, Fault{
				Field: "icons." + id,
				Message: fmt.Sprintf("unknown icon slot; the slots are %v",
					IconSlotIDs()),
			})
			continue
		}
		nodes, err := parseIconGeometry(decl.Icons[id])
		if err != nil {
			faults = append(faults, Fault{Field: "icons." + id, Message: err.Error()})
			continue
		}
		icons[id] = nodes
	}

	stroke, strokeFaults := resolveIconStroke(decl.Stroke)
	faults = append(faults, strokeFaults...)

	bevel, bevelFaults := resolveBevel(decl.Bevel)
	faults = append(faults, bevelFaults...)

	brand := DefaultBrand()
	if decl.Logomark != "" {
		if !ValidLogomarkVisibility(decl.Logomark) {
			faults = append(faults, Fault{
				Field: "brand.logomark",
				Message: fmt.Sprintf("%q is not one of %v — a theme may quiet the "+
					"product mark, never substitute one",
					decl.Logomark, LogomarkVisibilities()),
			})
		} else {
			brand.Logomark = decl.Logomark
		}
	}

	authored := map[string]Color{}
	// A token that was written but did not compile is reported once, for the
	// reason it failed, rather than also as unset.
	stated := map[string]bool{}
	for _, id := range sortedKeys(decl.Tokens) {
		tok, ok := BaseToken(id)
		if !ok {
			faults = append(faults, Fault{
				Field:   "tokens." + id,
				Message: "unknown token id",
			})
			continue
		}
		stated[tok.ID] = true
		if tok.Fill == FillBevel {
			faults = append(faults, Fault{
				Field: "tokens." + id,
				Message: "the host derives every bevel edge; set bevel.strength " +
					"to strengthen, soften, or flatten them",
			})
			continue
		}
		if tok.Fill == FillChange {
			faults = append(faults, Fault{
				Field: "tokens." + id,
				Message: "the host fits change fills to the syntax inks; set " +
					"diff-add-hue or diff-delete-hue to recolor them",
			})
			continue
		}
		color, err := ParseColor(decl.Tokens[id])
		if err != nil {
			faults = append(faults, Fault{Field: "tokens." + id, Message: err.Error()})
			continue
		}
		if tok.Opaque && color.A != 0xff {
			faults = append(faults, Fault{
				Field:   "tokens." + id,
				Message: "this plane must be opaque",
			})
			continue
		}
		authored[tok.ID] = color
	}

	for _, tok := range baseTokens {
		if tok.Fill != FillRequired {
			continue
		}
		if _, ok := authored[tok.ID]; !ok && !stated[tok.ID] {
			faults = append(faults, Fault{
				Field:   "tokens." + tok.ID,
				Message: "required token is unset",
			})
		}
	}

	authoredSyntax, syntaxFaults := parseSyntax(decl.Syntax)
	faults = append(faults, syntaxFaults...)

	// Stop before derivation when required inputs are invalid.
	if len(faults) > 0 {
		sortFaults(faults)
		return Compiled{}, &FaultsError{Faults: faults}
	}

	tokens := resolveTokens(authored, decl.Appearance, bevel)
	syntax := resolveSyntax(authoredSyntax, tokens)
	fitChangeFills(tokens, syntax, decl.Appearance)
	windowColors, colorFaults := resolveWindowColors(decl.WindowColors, tokens)
	agentColors, agentFaults := resolveAgentColors(decl.AgentColors, tokens)
	if colorFaults = append(colorFaults, agentFaults...); len(colorFaults) > 0 {
		sortFaults(colorFaults)
		return Compiled{}, &FaultsError{Faults: colorFaults}
	}

	compiled := Compiled{
		ID:           decl.ID,
		Name:         strings.TrimSpace(decl.Name),
		Appearance:   decl.Appearance,
		Tokens:       tokens,
		Syntax:       syntax,
		Brand:        brand,
		Icons:        icons,
		IconStroke:   stroke,
		Bevel:        bevel,
		WindowColors: windowColors,
		AgentColors:  agentColors,
	}
	if legibility := CheckLegibility(compiled); len(legibility) > 0 {
		sortFaults(legibility)
		return Compiled{}, &FaultsError{Faults: legibility}
	}
	return compiled, nil
}

// parseSyntax validates authored syntax scopes; unset scopes derive later.
func parseSyntax(declared map[string]SyntaxDeclaration) (map[string]SyntaxStyle, []Fault) {
	var faults []Fault
	authoredSyntax := map[string]SyntaxStyle{}
	for _, id := range sortedKeys(declared) {
		scope, ok := SyntaxScopeByID(id)
		if !ok {
			faults = append(faults, Fault{
				Field:   "syntax." + id,
				Message: "unknown syntax scope",
			})
			continue
		}
		entry := declared[id]
		style := SyntaxStyle{
			Italic: entry.Italic, Bold: entry.Bold, Underline: entry.Underline,
		}
		if scope.StyleOnly {
			if strings.TrimSpace(entry.Color) != "" {
				faults = append(faults, Fault{
					Field:   "syntax." + id,
					Message: "this scope carries style flags only; it never paints a color",
				})
				continue
			}
			authoredSyntax[id] = style
			continue
		}
		color, err := ParseColor(entry.Color)
		if err != nil {
			faults = append(faults, Fault{Field: "syntax." + id, Message: err.Error()})
			continue
		}
		style.Color = color
		authoredSyntax[id] = style
	}
	return authoredSyntax, faults
}

// resolveTokens fills unset tokens in dependency order.
func resolveTokens(authored map[string]Color, appearance Appearance, bevel Bevel) map[string]Color {
	out := make(map[string]Color, len(baseTokens))
	resolver := func(id string) Color { return out[id] }
	for _, tok := range baseTokens {
		if color, ok := authored[tok.ID]; ok {
			out[tok.ID] = color
			continue
		}
		// Change fills wait for the syntax plane.
		if tok.Fill == FillChange {
			continue
		}
		derived := tok.derive(resolver, appearance)
		if tok.Fill == FillBevel {
			derived = bevel.scale(derived)
		}
		out[tok.ID] = derived
	}
	return out
}

// resolveSyntax folds unset scopes in parent-first order.
func resolveSyntax(authored map[string]SyntaxStyle, tokens map[string]Color) map[string]SyntaxStyle {
	out := make(map[string]SyntaxStyle, len(syntaxScopes))
	for _, scope := range syntaxScopes {
		if entry, ok := authored[scope.ID]; ok {
			// Style-only scopes retain a folded color for total output.
			if scope.StyleOnly {
				entry.Color = foldColor(out, scope, tokens)
			}
			out[scope.ID] = entry
			continue
		}
		if scope.Parent != "" {
			out[scope.ID] = out[scope.Parent]
			continue
		}
		out[scope.ID] = SyntaxStyle{Color: foldColor(out, scope, tokens)}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortFaults(faults []Fault) {
	sort.Slice(faults, func(i, j int) bool {
		if faults[i].Field != faults[j].Field {
			return faults[i].Field < faults[j].Field
		}
		return faults[i].Message < faults[j].Message
	})
}

// foldColor returns the parent scope or body color.
func foldColor(resolved map[string]SyntaxStyle, scope SyntaxScope, tokens map[string]Color) Color {
	if scope.Parent != "" {
		return resolved[scope.Parent].Color
	}
	return tokens["text"]
}
