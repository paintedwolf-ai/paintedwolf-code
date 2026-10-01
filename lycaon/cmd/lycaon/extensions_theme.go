package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/theme"
)

// runExtNewTheme writes a theme unit prefilled from a stock palette.
func runExtNewTheme(args []string) error {
	fs := flag.NewFlagSet("new-theme", flag.ContinueOnError)
	from := fs.String("from", "", "stock theme to fork: daylight or charcoal")
	name := fs.String("name", "", "display name for the new theme")
	id := fs.String("id", "", "contribution id, provider:name")
	out := fs.String("out", "", "file to write (default: stdout)")
	full := fs.Bool("full", false, "include every derived token and syntax scope, not just the required set")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" || strings.TrimSpace(*id) == "" {
		return fmt.Errorf("--name and --id are required")
	}
	source := strings.TrimSpace(*from)
	if source == "" {
		source = "daylight"
	}

	root, err := moduleRootForThemes()
	if err != nil {
		return err
	}
	compiled, err := compileStockTheme(root, source)
	if err != nil {
		return err
	}

	body := themeScaffold(*id, *name, compiled, *full)
	if strings.TrimSpace(*out) == "" {
		fmt.Print(body)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(body), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s — every required token is present; run `pw extensions validate` to check contrast\n", *out)
	return nil
}

func moduleRootForThemes() (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := root; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "lycaon", "go.mod")); err == nil {
			return filepath.Join(dir, "lycaon"), nil
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", fmt.Errorf("run this from inside the repository")
		}
	}
}

func compileStockTheme(moduleRoot, stem string) (theme.Compiled, error) {
	path := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform",
		"contributions", "themes", stem+".yaml")
	body, err := os.ReadFile(path) // #nosec G304 -- bundled pack path
	if err != nil {
		return theme.Compiled{}, fmt.Errorf("unknown stock theme %q", stem)
	}
	decl, err := contribution.DecodeTheme(body)
	if err != nil {
		return theme.Compiled{}, err
	}
	return decl.Compiled()
}

// themeScaffold renders a compiled theme as an authorable unit.
func themeScaffold(id, name string, base theme.Compiled, full bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "id: %s\n", id)
	fmt.Fprintf(&b, "name: %q\n", name)
	fmt.Fprintf(&b, "appearance: %s\n\n", base.Appearance)

	writeWindowColorsScaffold(&b, base.WindowColors)
	if full || base.AgentColors != theme.DefaultAgentColors(base) {
		writeAgentColorsScaffold(&b, base.AgentColors)
	}
	if full || base.Bevel != theme.DefaultBevel() {
		minimum, maximum := theme.BevelStrengthRange()
		b.WriteString("# How strongly raised and recessed controls show their edges:\n")
		fmt.Fprintf(&b, "# %g is flat, 1 is the product's own, %g doubles them.\n", minimum, maximum)
		fmt.Fprintf(&b, "bevel:\n  strength: %g\n\n", base.Bevel.Strength)
	}

	b.WriteString("# Forked from the stock palette; compiles as-is.\n")
	if !full {
		b.WriteString("# A token its derivation reproduces is left out. --full lists the\n")
		b.WriteString("# complete vocabulary.\n")
	}
	recovered, canCompare := derivedBaseline(base)
	b.WriteString("tokens:\n")
	for _, tok := range theme.BaseTokens() {
		// Bevel edges derive from bevel.strength; change fills from the diff hues.
		if tok.Fill == theme.FillBevel || tok.Fill == theme.FillChange {
			continue
		}
		if !full && canCompare && tok.Fill != theme.FillRequired &&
			recovered.Tokens[tok.ID] == base.Tokens[tok.ID] {
			continue
		}
		fmt.Fprintf(&b, "  # %s\n", tok.Description)
		fmt.Fprintf(&b, "  %s: %q\n", tok.ID, base.Tokens[tok.ID].String())
	}

	b.WriteString("\n# Unset scopes fold to their parent, so a coarse theme is a complete one.\n")
	b.WriteString("syntax:\n")
	roots := make([]theme.SyntaxScope, 0)
	for _, scope := range theme.SyntaxScopes() {
		if !full && canCompare && scope.Parent != "" &&
			recovered.Syntax[scope.ID] == base.Syntax[scope.ID] {
			continue
		}
		roots = append(roots, scope)
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })
	for _, scope := range roots {
		style := base.Syntax[scope.ID]
		flags := make([]string, 0, 3)
		if style.Italic {
			flags = append(flags, "italic: true")
		}
		if style.Bold {
			flags = append(flags, "bold: true")
		}
		if style.Underline {
			flags = append(flags, "underline: true")
		}
		switch {
		case scope.StyleOnly && len(flags) == 0:
			continue
		case scope.StyleOnly:
			fmt.Fprintf(&b, "  %s: { %s }\n", scope.ID, strings.Join(flags, ", "))
		case len(flags) == 0:
			fmt.Fprintf(&b, "  %s: %q\n", scope.ID, style.Color.String())
		default:
			fmt.Fprintf(&b, "  %s: { color: %q, %s }\n",
				scope.ID, style.Color.String(), strings.Join(flags, ", "))
		}
	}
	return b.String()
}

// derivedBaseline resolves the theme's required decisions alone.
func derivedBaseline(base theme.Compiled) (theme.Compiled, bool) {
	decl := theme.Declaration{
		Name:       base.Name,
		Appearance: base.Appearance,
		Tokens:     map[string]string{},
	}
	for _, tok := range theme.BaseTokens() {
		if tok.Fill == theme.FillRequired {
			decl.Tokens[tok.ID] = base.Tokens[tok.ID].String()
		}
	}
	decl.Syntax = map[string]theme.SyntaxDeclaration{}
	for _, scope := range theme.SyntaxScopes() {
		if scope.Parent != "" {
			continue
		}
		style := base.Syntax[scope.ID]
		entry := theme.SyntaxDeclaration{
			Italic: style.Italic, Bold: style.Bold, Underline: style.Underline,
		}
		if !scope.StyleOnly {
			entry.Color = style.Color.String()
		}
		decl.Syntax[scope.ID] = entry
	}
	out, err := theme.Compile(decl)
	if err != nil {
		return theme.Compiled{}, false
	}
	return out, true
}

func writeWindowColorsScaffold(b *strings.Builder, colors theme.WindowColors) {
	fmt.Fprintf(b, "window_colors:\n  main: %q\n  anchors: [", colors.Main.String())
	for i, anchor := range colors.Anchors {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", anchor.String())
	}
	fmt.Fprintf(b, "]\n  hue_spread: %g\n  chroma_min: %g\n  chroma_max: %g\n\n", colors.HueSpread, colors.ChromaMin, colors.ChromaMax)
}

func writeAgentColorsScaffold(b *strings.Builder, colors theme.AgentColors) {
	fmt.Fprintf(b, "agent_colors:\n  main: %q\n  lightness_step: %g\n  chroma: %g\n\n", colors.Main.String(), colors.LightnessStep, colors.Chroma)
}
