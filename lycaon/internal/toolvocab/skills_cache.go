package toolvocab

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	"github.com/flosch/pongo2/v6"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type compiledSkillTemplate struct {
	tpl   *pongo2.Template
	roots []string
	err   error
}

type skillTextAnalysis struct {
	toolsIn             []string
	hyphenForms         []string
	hasEmptyHandle      bool
	handleTokens        []string
	blockCalls          [][]Call
	hasUnloadedCmdSpans bool
}

var compiledSkillTplCache = scopedstore.New[compiledSkillTemplate](256)

func skillSelectorRoot(sel string) string {
	for i := 0; i < len(sel); i++ {
		if sel[i] == '.' || sel[i] == ' ' {
			return sel[:i]
		}
	}
	return sel
}

func compileSkillTemplate(textHash, text string) compiledSkillTemplate {
	if hit, ok := compiledSkillTplCache.Load(textHash); ok {
		return hit
	}
	tpl, err := pongoplain.Compile(text)
	if err != nil {
		entry := compiledSkillTemplate{err: err}
		compiledSkillTplCache.Store(textHash, entry)
		return entry
	}
	rootSet := map[string]bool{}
	for _, sel := range pongoplain.VariableReferences(text) {
		if root := skillSelectorRoot(sel); root != "" {
			rootSet[root] = true
		}
	}
	roots := make([]string, 0, len(rootSet))
	for name := range rootSet {
		roots = append(roots, name)
	}
	slices.Sort(roots)
	entry := compiledSkillTemplate{tpl: tpl, roots: roots}
	compiledSkillTplCache.Store(textHash, entry)
	return entry
}

func projectedVarsDigestKey(vars map[string]any, roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	h := sha256.New()
	for _, name := range roots {
		v, ok := vars[name]
		_, _ = fmt.Fprintf(h, "%s=%t:%v\x00", name, ok, v)
	}
	return string(h.Sum(nil))
}

func renderSkillTextProjected(localCache map[string]skillRenderMemo, text string, vars map[string]any) (string, error) {
	sum := sha256.Sum256([]byte(text))
	textHash := string(sum[:])
	unit := compileSkillTemplate(textHash, text)
	if unit.err != nil {
		return "", unit.err
	}
	key := textHash + "\x00" + projectedVarsDigestKey(vars, unit.roots)
	if localCache != nil {
		if hit, ok := localCache[key]; ok {
			return hit.out, hit.err
		}
		out, err := pongoplain.ExecuteDetachedUnobserved(unit.tpl, vars)
		localCache[key] = skillRenderMemo{out: out, err: err}
		return out, err
	}
	if hit, ok := skillRenderCache.Load(key); ok {
		return hit.out, hit.err
	}
	out, err := pongoplain.ExecuteDetachedUnobserved(unit.tpl, vars)
	skillRenderCache.Store(key, skillRenderMemo{out: out, err: err})
	return out, err
}

func analyzeSkillText(c *Catalog, checkText string) skillTextAnalysis {
	sum := sha256.Sum256([]byte(checkText))
	key := string(sum[:])
	if hit, ok := c.textAnalysis.Load(key); ok {
		return hit
	}
	analysis := skillTextAnalysis{
		toolsIn:             ToolsIn(c, checkText),
		hasUnloadedCmdSpans: len(UnloadedCommandSpans(c, checkText)) > 0,
	}
	for _, name := range toolcontract.HyphenatedCommandFormsIn(checkText) {
		if !c.registered[name] && !c.operations[name] {
			analysis.hyphenForms = append(analysis.hyphenForms, name)
		}
	}
	if strings.IndexByte(checkText, '#') >= 0 {
		analysis.hasEmptyHandle = emptyHandle.MatchString(checkText)
		seen := map[string]bool{}
		for _, m := range handleToken.FindAllStringSubmatch(checkText, -1) {
			name := m[1]
			if seen[name] || name == evidence.KindSourceChange {
				continue
			}
			seen[name] = true
			analysis.handleTokens = append(analysis.handleTokens, name)
		}
	}
	for _, b := range Blocks(checkText) {
		if calls := Calls(c, b.Text); len(calls) > 0 {
			analysis.blockCalls = append(analysis.blockCalls, calls)
		}
	}
	c.textAnalysis.Store(key, analysis)
	return analysis
}
