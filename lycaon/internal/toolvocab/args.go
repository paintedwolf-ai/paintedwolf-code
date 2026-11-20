package toolvocab

import (
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/toolschema"
)

// ArgClaim is an argument rendered text attributes to a tool.
type ArgClaim struct {
	Tool, Arg string
}

var (
	callOpen   = regexp.MustCompile(`\b([a-z][a-z0-9_]*)\(`)
	callArg    = regexp.MustCompile(`(?:^|,)\s*([a-z_][a-z0-9_]*)\s*[=:]`)
	quoted     = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	dottedPath = regexp.MustCompile("`([a-z][a-z0-9_]*)\\.([a-z_][a-z0-9_]*)")
)

// ArgClaimsIn lists, sorted, the arguments text attributes to a known tool:
// the keyword arguments of a call written `tool(arg=…)` or `tool(arg: …)`, and
// the head of a backticked `tool.arg` path. Quoted values are not read.
func ArgClaimsIn(c *Catalog, text string) []ArgClaim {
	seen := map[ArgClaim]bool{}
	for _, loc := range callOpen.FindAllStringSubmatchIndex(text, -1) {
		tool := text[loc[2]:loc[3]]
		if !c.KnownName(tool) {
			continue
		}
		args := quoted.ReplaceAllString(callArguments(text[loc[1]:]), `""`)
		for _, m := range callArg.FindAllStringSubmatch(args, -1) {
			seen[ArgClaim{Tool: tool, Arg: m[1]}] = true
		}
	}
	for _, m := range dottedPath.FindAllStringSubmatch(text, -1) {
		if c.KnownName(m[1]) {
			seen[ArgClaim{Tool: m[1], Arg: m[2]}] = true
		}
	}
	out := make([]ArgClaim, 0, len(seen))
	for claim := range seen {
		out = append(out, claim)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Arg < out[j].Arg
	})
	return out
}

// callArguments is the argument list of a call opened just before rest, up to
// its closing parenthesis on the same line.
func callArguments(rest string) string {
	depth := 0
	for i, r := range rest {
		switch r {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return rest[:i]
			}
			depth--
		case '\n':
			return rest[:i]
		}
	}
	return rest
}

// UnacceptedArgs lists the claims in text that their tool's schema does not
// accept. schema reports a tool's argument schema; a tool it does not report
// is skipped, since the name check covers a tool the reader lacks.
func UnacceptedArgs(c *Catalog, text string, schema func(tool string) (map[string]any, bool)) []ArgClaim {
	var out []ArgClaim
	for _, claim := range ArgClaimsIn(c, text) {
		args, ok := schema(claim.Tool)
		if !ok || acceptsArg(args, claim.Arg) {
			continue
		}
		out = append(out, claim)
	}
	return out
}

func acceptsArg(schema map[string]any, arg string) bool {
	for _, path := range toolschema.ArgFieldPaths(schema) {
		if head, _, _ := strings.Cut(path, "."); head == arg {
			return true
		}
	}
	return false
}
