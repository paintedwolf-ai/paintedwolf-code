package contract

// AC-WR05-01: one path-addressing rule across tool schemas, operation schemas,
// language.json, generated command help, and none-classified path arguments.
// Bare paths and empty cwd mean the session folder, `@label/path` addresses any
// attached folder, `.` in discovery walks all attached folders, `@scratch/` and
// `@output/` address the virtual session folders, and absolute paths are
// accepted. A path-shaped argument that is deliberately not addressed (a key,
// filter, URL path, or reported metadata) must say so in its own description.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// pathAddressingPathProps names every schema property that carries a path
// argument. Object-typed properties (page_act's drag `to` element selector)
// are not path arguments.
var pathAddressingPathProps = map[string]struct{}{
	"path": {}, "paths": {}, "src": {}, "dest": {}, "file": {},
	"source": {}, "destination": {}, "path_a": {}, "path_b": {},
	"from": {}, "to": {}, "body_path": {},
}

// pathAddressingNonAddressedMarkers lists accepted wordings for a path-shaped
// argument that is deliberately not addressed through the session-folder rule.
var pathAddressingNonAddressedMarkers = []string{
	"not addressed paths", "not an addressed path", "not a filesystem path",
	"reservation key", "plan-store key", "recorded paths", "worker's session",
	"leg's session", "cited receipt", "session-folder-relative", "pathspec",
	"site-relative", "same addressing as path", "model-data key", "url path",
	"prefix filter", "conflict path", "extraction spec", "element selector",
	"display metadata", "git revision",
}

type pathAddressingArg struct {
	schema string // "tool" or "operation"
	name   string // schema stem
	prop   string // dotted property path inside the schema
	desc   string
}

// pathAddressingWalk collects every path-shaped property, including nested
// objects, arrays of objects, and oneOf/anyOf/allOf variants.
func pathAddressingWalk(schemaKind, name string, props map[string]any, prefix string, out *[]pathAddressingArg) {
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		prop, ok := props[n].(map[string]any)
		if !ok {
			continue
		}
		dotted := n
		if prefix != "" {
			dotted = prefix + "." + n
		}
		typ, _ := prop["type"].(string)
		if _, isPath := pathAddressingPathProps[n]; isPath && typ != "object" {
			desc, _ := prop["description"].(string)
			*out = append(*out, pathAddressingArg{schema: schemaKind, name: name, prop: dotted, desc: desc})
		}
		if sub, ok := prop["properties"].(map[string]any); ok {
			pathAddressingWalk(schemaKind, name, sub, dotted, out)
		}
		if items, ok := prop["items"].(map[string]any); ok {
			if sub, ok := items["properties"].(map[string]any); ok {
				pathAddressingWalk(schemaKind, name, sub, dotted, out)
			}
		}
		for _, key := range []string{"oneOf", "anyOf", "allOf"} {
			variants, ok := prop[key].([]any)
			if !ok {
				continue
			}
			for _, variant := range variants {
				vm, ok := variant.(map[string]any)
				if !ok {
					continue
				}
				if sub, ok := vm["properties"].(map[string]any); ok {
					pathAddressingWalk(schemaKind, name, sub, dotted, out)
				}
			}
		}
	}
}

// pathAddressingStated reports whether a description states the addressing
// rule or one of the documented non-addressed markers.
func pathAddressingStated(desc string) bool {
	lower := strings.ToLower(desc)
	if strings.Contains(lower, "session folder") && strings.Contains(lower, "@label") {
		return true
	}
	for _, marker := range pathAddressingNonAddressedMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func TestPathAddressingRuleStatedInSchemas(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	base := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools")
	for _, dir := range []struct{ name, label string }{
		{"schemas", "tool"}, {"operation-schemas", "operation"},
	} {
		schemas, err := toolschema.LoadSchemaDir(filepath.Join(base, dir.name))
		contractcheck.FailErr(t, "load "+dir.name, err)
		var args []pathAddressingArg
		names := make([]string, 0, len(schemas.Tools))
		for name := range schemas.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			props, ok := schemas.Tools[name].Schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		pathAddressingWalk(dir.label, name, props, "", &args)
		}
		if len(args) == 0 {
			t.Fatalf("no path arguments found in %s — the walk is broken", dir.name)
		}
		for _, arg := range args {
			if !pathAddressingStated(arg.desc) {
				t.Errorf("%s schema %s property %s does not state the session-folder/@label addressing rule or a documented non-addressed marker: %q",
					arg.schema, arg.name, arg.prop, arg.desc)
			}
		}
		t.Logf("%s: %d path arguments state the rule", dir.name, len(args))
	}
}

func TestPathAddressingRuleStatedInLanguageJSON(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "command-language-v1", "language.json")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read language.json", err)
	var doc struct {
		Rules map[string]string `json:"rules"`
	}
	contractcheck.FailErr(t, "decode language.json", json.Unmarshal(data, &doc))
	rule, ok := doc.Rules["paths"]
	if !ok {
		t.Fatal("language.json rules.paths is missing")
	}
	lower := strings.ToLower(rule)
	for _, want := range []string{
		"session folder", "empty cwd means the session folder", "@label", "@scratch", "@output", "absolute",
	} {
		if !strings.Contains(lower, want) {
			t.Errorf("language.json rules.paths does not state %q", want)
		}
	}
}

func TestPathAddressingRuleStatedInCommandHelp(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	contractcheck.FailErr(t, "load generated language catalog", err)
	// cd, pwd and native_path are the addressing commands; their generated
	// help must carry the same rule the schemas and language.json state.
	for _, name := range []string{"cd", "pwd", "native_path"} {
		help, ok := catalog.CommandHelp(name)
		if !ok {
			t.Errorf("no generated command help for %s", name)
			continue
		}
		lower := strings.ToLower(help)
		for _, want := range []string{"session folder", "@label", "@scratch", "@output"} {
			if !strings.Contains(lower, want) {
				t.Errorf("generated command help for %s does not state %q", name, want)
			}
		}
	}
}

// noneClassifiedPathArgEntry covers one none-classified path argument.
// resolver names the shared resolver the argument feeds: the test exercises
// that resolver with `@label` input. An empty resolver marks a deliberately
// non-addressed argument; marker must then appear in its description.
type noneClassifiedPathArgEntry struct {
	resolver string
	marker   string
}

// noneClassifiedPathArgs maps "schema/property" to its resolver coverage.
// Every path-shaped argument on a MultiRootNone tool or operation must appear
// here, so a new none-classified path argument fails this test until it is
// classified as addressed (with its resolver) or documented non-addressed.
var noneClassifiedPathArgs = map[string]noneClassifiedPathArgEntry{
	// Addressed through the shared resolvers.
	"capture_page/routes.body_path":            {resolver: "read"},
	"page_act/actions.routes.body_path":        {resolver: "read"},
	"page_open/routes.body_path":               {resolver: "read"},
	"fetch_url/dest":                           {resolver: "write"},
	"render_view/dest":                         {resolver: "write"},
	"http_request/body_path":                   {resolver: "bind"},
	"http_request/form.path":                   {resolver: "bind"},
	// Deliberately not addressed: keys, filters, URL paths, reported metadata.
	"capture_page/path":                        {marker: "url path"},
	"measure_page/path":                        {marker: "url path"},
	"page_open/path":                           {marker: "url path"},
	"complete_leg/coverage_gaps.paths":         {marker: "not addressed paths"},
	"complete_leg/coverage_review.assessments.cited_evidence.path": {marker: "cited receipt"},
	"complete_leg/findings.path":                                   {marker: "its receipt"},
	"fanout_plan/legs.scope.paths":                                 {marker: "leg's session"},
	"handoff_release/paths":                                        {marker: "not addressed paths"},
	"handoff_reserve/paths":                                        {marker: "not addressed paths"},
	"preview_overlay/path":                                         {marker: "not an addressed path"},
	"promote_overlay/resolutions.path":                             {marker: "not an addressed path"},
	"scan_query/selector.paths":                                    {marker: "not addressed paths"},
	"scan_query/path":                                              {marker: "not addressed paths"},
	"state_query/path":                                             {marker: "model-data key"},
	"state_update/path":                                            {marker: "model-data key"},
	"submit_verdict/cited_evidence.path":                           {marker: "cited receipt"},
	"task/scope.paths":                                             {marker: "worker's session"},
	"http_request/capture_tokens.from":                             {marker: "extraction spec"},
}

func TestNoneClassifiedPathArgsResolverCoverage(t *testing.T) {
	fix := newMultiRootFixture(t)
	schemas := contractToolSchemas(t)

	// Enumerate every path-shaped argument on none-classified tools and
	// operations straight from the schemas.
	found := map[string]pathAddressingArg{}
	for _, name := range toolcontract.MultiRootDeclaredNames() {
		capability, ok := toolcontract.MultiRootOf(name)
		if !ok || capability != toolcontract.MultiRootNone {
			continue
		}
		entry, ok := schemas.Tools[name]
		if !ok {
			t.Errorf("none-classified %q has no schema", name)
			continue
		}
		var args []pathAddressingArg
		props, ok := entry.Schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		pathAddressingWalk("none", name, props, "", &args)
		for _, arg := range args {
			found[arg.name+"/"+arg.prop] = arg
		}
	}
	if len(found) == 0 {
		t.Fatal("no none-classified path arguments found — the walk is broken")
	}

	for key, arg := range found {
		entry, ok := noneClassifiedPathArgs[key]
		if !ok {
			t.Errorf("none-classified path argument %s has no resolver coverage entry — add it to noneClassifiedPathArgs (addressed: name its resolver; otherwise document the non-addressed marker)", key)
			continue
		}
		if entry.resolver != "" {
			probeNoneClassifiedResolver(t, fix, key, entry.resolver)
			continue
		}
		if !strings.Contains(strings.ToLower(arg.desc), entry.marker) {
			t.Errorf("none-classified path argument %s is documented non-addressed but its description lacks the marker %q: %q", key, entry.marker, arg.desc)
		}
	}
	for key := range noneClassifiedPathArgs {
		if _, ok := found[key]; !ok {
			t.Errorf("noneClassifiedPathArgs entry %s matches no none-classified path argument — drop the stale entry", key)
		}
	}
}

// probeNoneClassifiedResolver exercises the resolver one none-classified path
// argument feeds, with `@label` input against a non-active attached folder and
// with an unknown label.
func probeNoneClassifiedResolver(t *testing.T, fix multiRootFixture, key, resolver string) {
	t.Helper()
	ctx := context.Background()
	tc := fix.tctx("s1")
	labeled := "@lycaon-den/secondary-sentinel.txt"
	want := filepath.Join(fix.secondaryDir, "secondary-sentinel.txt")
	unknown := "@missing/secondary-sentinel.txt"
	switch resolver {
	case "read":
		got, err := projectpaths.ResolveRead(ctx, nil, tc, labeled)
		if err != nil {
			t.Errorf("%s: read resolver refused @label input: %v", key, err)
		} else if got.Abs != want {
			t.Errorf("%s: read resolver = %q want %q", key, got.Abs, want)
		}
		_, err = projectpaths.ResolveRead(ctx, nil, tc, unknown)
		if code := toolRejectCode(err); code != "UNKNOWN_ROOT_LABEL" {
			t.Errorf("%s: unknown label code = %q want UNKNOWN_ROOT_LABEL (err=%v)", key, code, err)
		}
	case "write":
		target := "@lycaon-den/write-probe.txt"
		got, err := projectpaths.ResolveWrite(ctx, nil, tc, target)
		if err != nil {
			t.Errorf("%s: write resolver refused @label input: %v", key, err)
		} else if got.Abs != filepath.Join(fix.secondaryDir, "write-probe.txt") {
			t.Errorf("%s: write resolver = %q want %q", key, got.Abs, filepath.Join(fix.secondaryDir, "write-probe.txt"))
		}
		_, err = projectpaths.ResolveWrite(ctx, nil, tc, unknown)
		if code := toolRejectCode(err); code != "UNKNOWN_ROOT_LABEL" {
			t.Errorf("%s: unknown label code = %q want UNKNOWN_ROOT_LABEL (err=%v)", key, code, err)
		}
	case "bind":
		paths, err := projectpaths.NewProgramPaths(ctx, nil, tc, "")
		if err != nil {
			t.Fatalf("%s: bind invocation failed: %v", key, err)
		}
		// The command language expands an unquoted @label through Address
		// before the operand reaches Bind.
		abs, ok, err := paths.Address(ctx, "lycaon-den")
		if err != nil || !ok {
			t.Errorf("%s: address expansion refused @label input: ok=%v err=%v", key, ok, err)
		} else {
			bound, err := paths.Bind(ctx, filepath.Join(abs, "secondary-sentinel.txt"), false)
			if err != nil {
				t.Errorf("%s: bind refused expanded @label input: %v", key, err)
			} else if bound.Resolved.Abs != want {
				t.Errorf("%s: bind = %q want %q", key, bound.Resolved.Abs, want)
			}
		}
		if _, ok, _ := paths.Address(ctx, "missing"); ok {
			t.Errorf("%s: unknown label expanded; want literal text", key)
		}
	default:
		t.Fatalf("%s: unknown resolver kind %q", key, resolver)
	}
}
