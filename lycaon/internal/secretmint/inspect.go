package secretmint

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/ptyinput"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// Candidate holds a credential-slot assignment and plaintext for fingerprinting.
type Candidate struct {
	Assignment string
	Value      string
	inspector  *Inspector
}

// Inspect scans structured keys and catalogued argument formats.
func (i *Inspector) Inspect(tool string, args map[string]any) []Candidate {
	if i == nil || len(args) == 0 {
		return nil
	}
	hits := i.inspectStructured(args)
	for _, surface := range i.surfaces[strings.TrimSpace(tool)] {
		hits = append(hits, i.inspectSurface(surface, args)...)
	}
	return uniqueCandidates(hits)
}

func (i *Inspector) inspectSurface(surface Surface, args map[string]any) []Candidate {
	var hits []Candidate
	switch surface.Kind {
	case "command":
		if plan, err := commandsurface.ParsePlan(args); err == nil {
			for _, st := range plan.Stages {
				hits = append(hits, i.inspectStage(st)...)
			}
		} else if raw, ok := args["command"].(string); ok {
			hits = append(hits, i.inspectShellText(raw)...)
		}
	case "terminal":
		typed, _ := args[surface.ContentArg].(string)
		for _, line := range ptyinput.DecodeLines(typed) {
			hits = append(hits, i.inspectShellText(line)...)
		}
	case "content":
		text, _ := args[surface.ContentArg].(string)
		hits = append(hits, i.inspectFileText(text)...)
	}
	return hits
}

func (i *Inspector) inspectStage(st exec.Stage) []Candidate {
	var hits []Candidate
	if len(st.Env) > 0 {
		keys := make([]string, 0, len(st.Env))
		for k := range st.Env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			hits = append(hits, i.hitEnv(k, st.Env[k])...)
		}
	}
	toks := make([]string, 0, 1+len(st.Args))
	if st.Name != "" {
		toks = append(toks, st.Name)
	}
	toks = append(toks, st.Args...)
	hits = append(hits, i.inspectTokens(toks, strings.Join(toks, " "))...)
	return hits
}

func (i *Inspector) inspectShellText(raw string) []Candidate {
	var hits []Candidate
	for _, line := range commandsurface.SplitCommandLines(raw) {
		for _, group := range commandsurface.SplitExecutionGroups(line) {
			for _, stage := range commandsurface.SplitPipelineStages(group) {
				stripped := make([]string, len(stage))
				for j, tok := range stage {
					stripped[j] = stripOneQuoteLayer(tok)
				}
				hits = append(hits, i.inspectTokens(stripped, strings.Join(stage, " "))...)
			}
		}
	}
	return hits
}

func (i *Inspector) inspectTokens(toks []string, commandLine string) []Candidate {
	assigns, rest := peelAssignments(toks)
	var hits []Candidate
	for _, pair := range assigns {
		hits = append(hits, i.hitEnv(pair[0], pair[1])...)
	}
	if len(rest) == 0 {
		return hits
	}
	image := strings.ToLower(filepath.Base(rest[0]))
	hits = append(hits, i.inspectImage(image, rest[1:])...)
	hits = append(hits, i.inspectSQL(commandLine)...)
	return hits
}

func (i *Inspector) inspectImage(image string, args []string) []Candidate {
	var hits []Candidate
	for _, spec := range i.catalog.CommandImages {
		if strings.ToLower(spec.Image) != image {
			continue
		}
		if !hasAllFlags(args, spec.RequireFlags) {
			continue
		}
		hits = append(hits, i.collectFlagValues(args, spec.ValueFlags)...)
		if spec.Value == valueLastPositional {
			if v, ok := lastPositional(args); ok {
				hits = append(hits, i.candidate(spec.Image, v)...)
			}
		}
	}
	hits = append(hits, i.collectCredentialArguments(args)...)
	return hits
}

func (i *Inspector) collectFlagValues(args, flags []string) []Candidate {
	if len(flags) == 0 {
		return nil
	}
	var hits []Candidate
	for idx := 0; idx < len(args); idx++ {
		tok := args[idx]
		if tok == "--" {
			break
		}
		name, val, attached := splitFlag(tok)
		if !slices.Contains(flags, name) {
			continue
		}
		if attached {
			hits = append(hits, i.candidate(name, val)...)
			continue
		}
		if idx+1 >= len(args) {
			continue
		}
		idx++
		hits = append(hits, i.candidate(name, args[idx])...)
	}
	return hits
}

// splitFlag recognizes attached values only in the --flag=value form.
func splitFlag(tok string) (name, val string, attached bool) {
	if strings.HasPrefix(tok, "--") {
		if eq := strings.IndexByte(tok, '='); eq >= 0 {
			return tok[:eq], tok[eq+1:], true
		}
	}
	return tok, "", false
}

func lastPositional(args []string) (string, bool) {
	var pos []string
	for idx := 0; idx < len(args); idx++ {
		tok := args[idx]
		if tok == "--" {
			pos = append(pos, args[idx+1:]...)
			break
		}
		if strings.HasPrefix(tok, "-") {
			if strings.HasPrefix(tok, "--") && strings.Contains(tok, "=") {
				continue
			}
			if idx+1 < len(args) && !strings.HasPrefix(args[idx+1], "-") {
				idx++
			}
			continue
		}
		pos = append(pos, tok)
	}
	if len(pos) == 0 {
		return "", false
	}
	return pos[len(pos)-1], true
}

func hasAllFlags(args, need []string) bool {
	if len(need) == 0 {
		return true
	}
	seen := map[string]struct{}{}
	for _, tok := range args {
		if tok == "--" {
			break
		}
		name, _, _ := splitFlag(tok)
		seen[name] = struct{}{}
	}
	for _, n := range need {
		if _, ok := seen[n]; !ok {
			return false
		}
	}
	return true
}

func (i *Inspector) inspectSQL(commandLine string) []Candidate {
	var hits []Candidate
	for _, frag := range i.catalog.SQLMint {
		val, ok := quotedAfter(commandLine, frag.Pattern)
		if !ok {
			continue
		}
		hits = append(hits, i.candidate(frag.Pattern, val)...)
	}
	return hits
}

func quotedAfter(s, pattern string) (string, bool) {
	if pattern == "" {
		return "", false
	}
	lower := strings.ToLower(s)
	idx := strings.Index(lower, strings.ToLower(pattern))
	if idx < 0 {
		return "", false
	}
	rest := s[idx+len(pattern):]
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c != '\'' && c != '"' {
			continue
		}
		j := i + 1
		for j < len(rest) && rest[j] != c {
			j++
		}
		if j >= len(rest) {
			return "", false
		}
		return rest[i+1 : j], true
	}
	return "", false
}

func (i *Inspector) hitEnv(key, val string) []Candidate {
	canon, ok := i.credentialSlot(key, i.envKeys)
	if !ok {
		return nil
	}
	return i.candidate(canon, val)
}

// candidate excludes references and indirection without filtering on strength.
func (i *Inspector) candidate(assignment, value string) []Candidate {
	original := stripOneQuoteLayer(strings.TrimSpace(value))
	if original == "" || hasShellIndirection(original) || secretmatch.ContainsReferenceToken(original) {
		return nil
	}
	return []Candidate{{Assignment: assignment, Value: original, inspector: i}}
}

func peelAssignments(toks []string) (assigns [][2]string, rest []string) {
	rest = toks
	for len(rest) > 0 {
		key, val, ok := splitEnvAssignment(rest[0])
		if !ok {
			break
		}
		assigns = append(assigns, [2]string{key, val})
		rest = rest[1:]
	}
	return assigns, rest
}

func splitEnvAssignment(tok string) (key, val string, ok bool) {
	tok = stripOneQuoteLayer(tok)
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return "", "", false
	}
	key = tok[:eq]
	if !envName(key) {
		return "", "", false
	}
	return key, tok[eq+1:], true
}

func envName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !(unicode.IsLetter(r) || r == '_') {
				return false
			}
			continue
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}
