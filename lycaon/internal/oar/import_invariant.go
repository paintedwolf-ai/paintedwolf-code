package oar

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// InvariantImport maps the supported import syntax to an OAR document.
//
// Supported:
//
//	raise "RULE_ID_OR_MESSAGE" if:
//	    (call: ToolCall)
//	    call.name == "command"
//
//	raise "MSG" if:
//	    (a: ToolCall) -> (b: ToolCall)
//	    a.name == "read"
//	    b.name == "write"
type InvariantImport struct {
	ID      string
	Title   string
	Anchor  string
	When    string
	Flow    []string
	Effect  Effect
	Kind    Kind
	Emit    string
	Message string
}

var (
	reRaise     = regexp.MustCompile(`(?m)^\s*raise\s+"([^"]+)"\s+if\s*:\s*$`)
	reBinding   = regexp.MustCompile(`\((\w+)\s*:\s*(ToolCall|Message|ToolResult)\)`)
	reNameEq    = regexp.MustCompile(`(?m)^\s*(\w+)\.name\s*==\s*"([^"]+)"\s*$`)
	reNameIn    = regexp.MustCompile(`(?m)^\s*(\w+)\.name\s+in\s+\[([^\]]+)\]\s*$`)
	reFlowArrow = regexp.MustCompile(`->`)
)

// ImportInvariantSubset parses one supported rule into an OAR import.
func ImportInvariantSubset(src string) (*InvariantImport, error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, fmt.Errorf("empty invariant rule")
	}
	m := reRaise.FindStringSubmatch(src)
	if m == nil {
		return nil, fmt.Errorf(`invariant subset requires a raise "..." if: clause`)
	}
	title := m[1]
	idx := strings.Index(src, m[0])
	body := src
	if idx >= 0 {
		body = src[idx+len(m[0]):]
	}

	bindings := map[string]string{}
	var order []string
	for _, bm := range reBinding.FindAllStringSubmatch(body, -1) {
		name, typ := bm[1], bm[2]
		if _, ok := bindings[name]; ok {
			return nil, fmt.Errorf("duplicate binding %q", name)
		}
		bindings[name] = typ
		order = append(order, name)
	}
	if len(bindings) == 0 {
		return nil, fmt.Errorf("invariant subset requires at least one (name: ToolCall|Message|ToolResult) binding")
	}
	for _, typ := range bindings {
		if typ != "ToolCall" {
			return nil, fmt.Errorf("invariant subset currently supports ToolCall bindings only (got %s)", typ)
		}
	}

	namesByVar := map[string][]string{}
	for _, em := range reNameEq.FindAllStringSubmatch(body, -1) {
		namesByVar[em[1]] = []string{em[2]}
	}
	for _, im := range reNameIn.FindAllStringSubmatch(body, -1) {
		parts := splitQuotedList(im[2])
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty name list for %s", im[1])
		}
		namesByVar[im[1]] = parts
	}
	for _, v := range order {
		if _, ok := namesByVar[v]; !ok {
			return nil, fmt.Errorf("binding %q needs %s.name == \"...\" or in [...]", v, v)
		}
	}

	out := &InvariantImport{
		Title:   title,
		Effect:  EffectBlock,
		Kind:    KindInvariant,
		Emit:    "rule:imported",
		Message: title,
		ID:      invariantID(title),
	}

	if reFlowArrow.MatchString(body) {
		if len(order) < 2 {
			return nil, fmt.Errorf("flow (->) requires at least two ToolCall bindings")
		}
		for _, v := range order {
			ns := namesByVar[v]
			if len(ns) != 1 {
				return nil, fmt.Errorf("flow step %q must pin a single tool name", v)
			}
			out.Flow = append(out.Flow, ns[0])
		}
		out.Anchor = AnchorToolPreInvoke
		out.When = "true"
		return out, nil
	}

	v := order[0]
	ns := namesByVar[v]
	if len(ns) == 1 {
		out.Anchor = AnchorToolPreInvoke
		out.When = fmt.Sprintf(`tool == %q`, ns[0])
	} else {
		out.Anchor = AnchorToolPreInvoke
		quoted := make([]string, len(ns))
		for i, n := range ns {
			quoted[i] = fmt.Sprintf("%q", n)
		}
		out.When = fmt.Sprintf("tool in [%s]", strings.Join(quoted, ", "))
	}
	return out, nil
}

func splitQuotedList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"'`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func invariantID(title string) string {
	var b strings.Builder
	b.WriteString("IMPORTED_")
	needSep := false
	for _, r := range strings.ToUpper(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if needSep {
				b.WriteByte('_')
				needSep = false
			}
			b.WriteRune(r)
			continue
		}
		needSep = true
	}
	id := strings.Trim(b.String(), "_")
	if id == "IMPORTED" || id == "" {
		return "IMPORTED_RULE"
	}
	for strings.Contains(id, "__") {
		id = strings.ReplaceAll(id, "__", "_")
	}
	return id
}

// ToRule materializes an imported rule.
func (imp *InvariantImport) ToRule() *Rule {
	if imp == nil {
		return nil
	}
	r := &Rule{
		OAR:         CurrentOARVersion,
		ID:          imp.ID,
		Title:       imp.Title,
		Kind:        imp.Kind,
		Anchor:      imp.Anchor,
		When:        imp.When,
		Flow:        append([]string(nil), imp.Flow...),
		Effect:      imp.Effect,
		Enforcement: "enforce",
		OnError:     "fail_closed",
		Emit:        imp.Emit,
		What:        imp.Message,
		Tier:        TierPack,
		Source:      "import:invariant",
	}
	if r.Kind == "" {
		r.Kind = KindInvariant
	}
	if r.Effect == "" {
		r.Effect = EffectBlock
	}
	if r.Emit == "" {
		r.Emit = "rule:imported"
	}
	return r
}

// ToHintYAML emits a catalog-shaped hint YAML document for the imported rule.
func (imp *InvariantImport) ToHintYAML() ([]byte, error) {
	r := imp.ToRule()
	if r == nil {
		return nil, fmt.Errorf("nil import")
	}
	var b strings.Builder
	b.WriteString("hint_codes:\n")
	fmt.Fprintf(&b, "  %s:\n", r.ID)
	fmt.Fprintf(&b, "    oar: %q\n", r.OAR)
	fmt.Fprintf(&b, "    id: %s\n", r.ID)
	fmt.Fprintf(&b, "    kind: %s\n", r.Kind)
	fmt.Fprintf(&b, "    anchor: %s\n", r.Anchor)
	if req := RequiresFor(r.When, r.Flow, r.Selector); len(req.Profiles) > 0 || len(req.Facts) > 0 {
		b.WriteString("    requires:\n")
		if len(req.Profiles) > 0 {
			b.WriteString("      profiles:\n")
			for _, p := range req.Profiles {
				fmt.Fprintf(&b, "        - %s\n", p)
			}
		}
		if len(req.Facts) > 0 {
			b.WriteString("      facts:\n")
			for _, f := range req.Facts {
				fmt.Fprintf(&b, "        - %s\n", f)
			}
		}
	}
	fmt.Fprintf(&b, "    when: %q\n", r.When)
	if len(r.Flow) > 0 {
		b.WriteString("    flow:\n")
		for _, step := range r.Flow {
			fmt.Fprintf(&b, "      - %s\n", step)
		}
	}
	fmt.Fprintf(&b, "    effect: %s\n", r.Effect)
	// Presentation copy is a closed nested object ([OAR-DOC-24]); emit and
	// category are this engine's own and travel as extensions ([OAR-DOC-26]).
	b.WriteString("    copy:\n")
	fmt.Fprintf(&b, "      title: %q\n", r.Title)
	fmt.Fprintf(&b, "      what: %q\n", r.What)
	fmt.Fprintf(&b, "    x-paintedwolf-emit: %s\n", r.Emit)
	b.WriteString("    x-paintedwolf-category: recoverable\n")
	return []byte(b.String()), nil
}
