package contract

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestToolSchemaArgPropertiesHaveDescriptions walks every property in
// tools/schemas (including nested object/array item properties) and requires
// a non-empty description. Discovery is systems-driven from the loaded config —
// no allowlist of tools or args.
func TestToolSchemaArgPropertiesHaveDescriptions(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	if cfg == nil || len(cfg.Tools) == 0 {
		t.Fatal("tools/schemas loaded empty tools map")
	}

	names := make([]string, 0, len(cfg.Tools))
	for name := range cfg.Tools {
		names = append(names, name)
	}
	sort.Strings(names)

	var violations []string
	for _, name := range names {
		entry := cfg.Tools[name]
		if strings.TrimSpace(entry.Description) == "" {
			violations = append(violations, fmt.Sprintf("%s: tool description is empty", name))
		}
		collectUndescribedProperties(name, "", entry.Schema, &violations)
		// Fragments under $defs compose model-facing members, so they carry
		// descriptions too.
		if defs, ok := entry.Schema["$defs"].(map[string]any); ok {
			collectUndescribedProperties(name, "$defs", map[string]any{"properties": defs}, &violations)
		}
	}
	contractcheck.FailViolations(t, "tools/schemas properties missing non-empty description", violations)
}

type toolSchemaArgDesc struct {
	tool, path string
	feats      map[string]struct{}
}

// TestToolSchemaDescriptionsAvoidToolArgDupes fails when an arg description mostly
// restates its own tool blurb. Similarity is containment of the arg's word bigram and
// trigram set in the tool's; a same-tool pair fails above the 99.9th percentile of
// scores against every other tool's description.
func TestToolSchemaDescriptionsAvoidToolArgDupes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)

	toolFeats := map[string]map[string]struct{}{}
	var args []toolSchemaArgDesc

	names := make([]string, 0, len(cfg.Tools))
	for name := range cfg.Tools {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := cfg.Tools[name]
		toolFeats[name] = descriptionNGrams(entry.Description)
		collectDescribedArgs(name, "", entry.Schema, &args)
	}
	if len(args) == 0 || len(toolFeats) < 2 {
		t.Fatal("tools/schemas produced no comparable tool/arg descriptions")
	}

	var nullScores []float64
	type sameHit struct {
		tool, path string
		score      float64
	}
	var sameScores []sameHit

	for _, arg := range args {
		if score, ok := ngramContainment(arg.feats, toolFeats[arg.tool]); ok {
			sameScores = append(sameScores, sameHit{tool: arg.tool, path: arg.path, score: score})
		}
		for _, other := range names {
			if other == arg.tool {
				continue
			}
			if score, ok := ngramContainment(arg.feats, toolFeats[other]); ok {
				nullScores = append(nullScores, score)
			}
		}
	}
	if len(nullScores) == 0 {
		t.Fatal("null cross-tool score distribution is empty")
	}

	threshold := percentile(nullScores, 0.999)
	var violations []string
	for _, hit := range sameScores {
		if hit.score > threshold {
			violations = append(violations, fmt.Sprintf(
				"%s.%s: same-tool n-gram containment %.3f exceeds cross-tool null p99.9=%.3f",
				hit.tool, hit.path, hit.score, threshold,
			))
		}
	}
	contractcheck.FailViolations(t, "tools/schemas arg description restates its tool blurb (null-model outlier)", violations)
}

func collectUndescribedProperties(tool, prefix string, node map[string]any, out *[]string) {
	if node == nil {
		return
	}
	props, _ := node["properties"].(map[string]any)
	if props == nil {
		return
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		raw := props[key]
		child, ok := raw.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s.%s: property schema must be a map with description", tool, path))
			continue
		}
		desc, _ := child["description"].(string)
		if strings.TrimSpace(desc) == "" {
			*out = append(*out, fmt.Sprintf("%s.%s", tool, path))
		}
		collectUndescribedProperties(tool, path, child, out)
		if items, ok := child["items"].(map[string]any); ok {
			collectUndescribedProperties(tool, path+"[]", items, out)
		}
	}
}

func collectDescribedArgs(tool, prefix string, node map[string]any, out *[]toolSchemaArgDesc) {
	if node == nil {
		return
	}
	props, _ := node["properties"].(map[string]any)
	if props == nil {
		return
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		child, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		desc, _ := child["description"].(string)
		if strings.TrimSpace(desc) != "" {
			*out = append(*out, toolSchemaArgDesc{
				tool:  tool,
				path:  path,
				feats: descriptionNGrams(desc),
			})
		}
		collectDescribedArgs(tool, path, child, out)
		if items, ok := child["items"].(map[string]any); ok {
			collectDescribedArgs(tool, path+"[]", items, out)
		}
	}
}

func descriptionWords(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteByte(' ')
		}
	}
	parts := strings.Fields(b.String())
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) >= 3 {
			out = append(out, p)
		}
	}
	return out
}

func descriptionNGrams(s string) map[string]struct{} {
	words := descriptionWords(s)
	out := make(map[string]struct{})
	for n := 2; n <= 3; n++ {
		if len(words) < n {
			continue
		}
		for i := 0; i+n <= len(words); i++ {
			out[strings.Join(words[i:i+n], " ")] = struct{}{}
		}
	}
	return out
}

// ngramContainment is |arg ∩ tool| / |arg|. ok=false when the arg has too little
// n-gram signal to judge (avoids scoring empty/tiny labels).
func ngramContainment(arg, tool map[string]struct{}) (float64, bool) {
	if len(arg) < 3 {
		return 0, false
	}
	shared := 0
	for g := range arg {
		if _, ok := tool[g]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(arg)), true
}

func percentile(scores []float64, p float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	sorted := append([]float64(nil), scores...)
	sort.Float64s(sorted)
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
