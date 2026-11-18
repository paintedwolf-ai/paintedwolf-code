// Package closure holds the laws that close a host registry over its
// producers and readers: what the catalog declares is produced, read and
// present on disk, and nothing outside it is named.
package closure

import (
	"context"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/summarize"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// Family is the closure rule table.
var Family = laws.Family{Name: "closure", Rules: []laws.Rule{
	{Name: "capture-scripts-name-only-catalog-files", Anchors: []laws.Anchor{laws.Func("internal/debugpaths", "Files")}, Check: captureScriptsNameOnlyCatalogFiles},
	{Name: "evidence-producers-match-fingerprint-arms", Anchors: []laws.Anchor{laws.Func("internal/evidence", "buildEvidenceRecord")}, Check: evidenceProducersMatchFingerprintArms},
	{Name: "summarize-caps-are-read", Anchors: []laws.Anchor{laws.Decl("internal/summarize", "Caps")}, Exempt: []laws.Exemption{
		{Key: "Version", Reason: "the schema version LoadCaps checks"},
	}, Check: summarizeCapsAreRead},
	{Name: "event-topics-have-publish-sites", Anchors: []laws.Anchor{laws.Decl("pkg/api", "EventTopic")}, Check: eventTopicsHavePublishSites},
	{Name: "scanner-assets-live-where-the-host-reads-them", Anchors: []laws.Anchor{laws.Dir("lycaon/config/runtime/scanners")}, Check: scannerAssetsLiveWhereTheHostReadsThem},
	{Name: "launch-workflows-are-in-the-product-catalog", Anchors: []laws.Anchor{laws.Decl("internal/workflow/catalog", "Resolver.ListResolved")}, Check: launchWorkflowsAreInTheProductCatalog},
	{Name: "ambient-attach-is-a-bijection", Anchors: []laws.Anchor{laws.Func("internal/workflow/definition", "ValidateAmbientAttachBijection")}, Check: ambientAttachIsABijection},
}}

var scriptWord = regexp.MustCompile(`[A-Za-z0-9._/-]+`)

// The capture scripts cannot import the Go catalog, so each capture file and
// redirect variable they name must be one internal/debugpaths declares.
func captureScriptsNameOnlyCatalogFiles(r *laws.Run) {
	names, envs := map[string]bool{}, map[string]bool{}
	for _, f := range debugpaths.Files() {
		names[f.Name] = true
		if f.FileEnv != "" {
			envs[f.FileEnv] = true
		}
	}
	for _, rel := range []string{"scripts/debug-session.sh", "scripts/debug-capture.sh"} {
		for _, word := range scriptWord.FindAllString(r.Text(rel), -1) {
			base := word[strings.LastIndexByte(word, '/')+1:]
			switch {
			case (strings.HasSuffix(word, ".jsonl") || strings.HasSuffix(word, ".log")) && !names[base]:
				r.Report("%s writes %s, which the debug path catalog does not declare", rel, word)
			case strings.HasPrefix(word, "LYCAON_") && strings.HasSuffix(word, "_FILE") && !envs[word]:
				r.Report("%s exports %s, which the debug path catalog does not declare", rel, word)
			}
		}
	}
}

// Each evidence-producing tool the binding declares has an arm in the
// fingerprint indexer's switch on toolName, and each arm a declared tool.
func evidenceProducersMatchFingerprintArms(r *laws.Run) {
	binding, err := evidence.LoadBinding()
	r.Must(err)
	var arms []string
	ast.Inspect(r.FuncDecl("internal/evidence", "buildEvidenceRecord").Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "toolName" {
			return true
		}
		for _, stmt := range sw.Body.List {
			for _, expr := range stmt.(*ast.CaseClause).List {
				if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					name, err := strconv.Unquote(lit.Value)
					r.Must(err)
					arms = append(arms, name)
				}
			}
		}
		return false
	})
	r.Expect(len(arms), "toolName arms in buildEvidenceRecord")
	r.Diff("evidence-producing tools vs fingerprint arms", binding.DeclaredToolNames(), arms)
}

// Every summarize.yaml cap leaf is read by production code outside the
// summarize package's cap loader.
func summarizeCapsAreRead(r *laws.Run) {
	caps := r.TypedPackage("internal/summarize").Types.Scope().Lookup("Caps")
	read := map[string]bool{}
	for _, pkg := range r.TypedPackages("internal") {
		for sel, s := range pkg.Info.Selections {
			if s.Kind() == types.FieldVal && within(s.Recv(), caps.Type()) && !strings.HasSuffix(r.Types().Fset.Position(sel.Pos()).Filename, "/summarize/caps.go") {
				read[s.Obj().Name()] = true
			}
		}
	}
	for _, name := range capLeaves(reflect.TypeOf(summarize.Caps{})) {
		if !read[name] && !r.Exempt(name) {
			r.Report("summarize cap field %s is defined but never read", name)
		}
	}
}

// within reports whether typ is root or a struct reachable through root's
// fields.
func within(typ, root types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	if types.Identical(typ, root) {
		return true
	}
	st, ok := root.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := range st.NumFields() {
		if _, ok := st.Field(i).Type().Underlying().(*types.Struct); ok && within(typ, st.Field(i).Type()) {
			return true
		}
	}
	return false
}

func capLeaves(typ reflect.Type) []string {
	var out []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		if tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); tag == "" || tag == "-" {
			continue
		}
		if f.Type.Kind() == reflect.Struct {
			out = append(out, capLeaves(f.Type)...)
			continue
		}
		out = append(out, f.Name)
	}
	return out
}

// publishMethods take the topic as an argument: the event hub's Publish and
// the durable outbox's enqueues.
var publishMethods = []string{"Publish", "Enqueue", "EnqueueTx"}

// Every declared event topic is passed to a publish or enqueue call in
// production code.
func eventTopicsHavePublishSites(r *laws.Run) {
	published := map[string]bool{}
	for _, pkg := range r.TypedPackages("internal") {
		for _, file := range pkg.Syntax() {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || !slices.Contains(publishMethods, sel.Sel.Name) {
					return true
				}
				for _, arg := range call.Args {
					if tv, ok := pkg.Info.Types[arg]; ok && tv.Value != nil && isEventTopic(tv.Type) {
						published[strings.Trim(tv.Value.ExactString(), `"`)] = true
					}
				}
				return true
			})
		}
	}
	for _, topic := range api.AllEventTopicValues() {
		if !published[string(topic)] {
			r.Report("event topic %s has no publish or enqueue call in internal/", topic)
		}
	}
}

func isEventTopic(typ types.Type) bool {
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Name() == "EventTopic" && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "github.com/lycaon/lycaon/pkg/api"
}

var (
	scriptConfigPath = regexp.MustCompile(`config/(?:runtime|packs|scanners)/[A-Za-z0-9._/-]+`)
	goScannerAsset   = regexp.MustCompile(`"((?:[A-Za-z0-9._/-]*/)?(?:bundled-manifest|opengrep-gates|gates|runner|scan-hints|suppressions|rules-provenance|scanners)\.yaml)"`)
)

// Scanner engines and their assets live under config/runtime/scanners; packs
// carry scan guidance only, and every path a script or Go file names exists.
func scannerAssetsLiveWhereTheHostReadsThem(r *laws.Run) {
	exists := func(rel string) bool { _, err := os.Stat(r.Path(rel)); return err == nil }
	for _, f := range r.Tree("lycaon/config/packs/painted-wolf", ".yaml") {
		if strings.HasSuffix(f.Rel, "extension.yaml") && strings.Contains(f.Text(), "requires_scanners:") {
			r.Report("stock pack %s ships requires_scanners", f.Rel)
		}
	}
	for _, e := range r.Entries("lycaon/config/packs") {
		rel := strings.TrimPrefix(e.Rel, "lycaon/config/packs/")
		parts := strings.Split(rel, "/")
		switch {
		case e.Mode.IsDir() && strings.HasSuffix(rel, "/scanners/engines"):
			r.Report("packs may not ship engines: %s", e.Rel)
		case !e.Mode.IsDir() && len(parts) == 3 && parts[2] == "scanners.yaml":
			r.Report("packs may not ship a root scanners.yaml: %s", e.Rel)
		}
	}
	packs, err := extpacks.DiscoverStock()
	r.Must(err)
	guidance := false
	for _, p := range packs {
		switch p.ID {
		case "painted-wolf/scanners":
			r.Report("painted-wolf/scanners is discovered; it was renamed painted-wolf/scan-guidance")
		case "painted-wolf/scan-guidance":
			guidance = true
		}
	}
	if !guidance {
		r.Report("painted-wolf/scan-guidance is not discovered")
	}
	for _, rel := range []string{"scanners.yaml", "bundled-manifest.yaml", "opengrep-gates.yaml", "rules-provenance.yaml", "rules/lycaon", "rules/vendor"} {
		if !exists("lycaon/config/runtime/scanners/" + rel) {
			r.Report("stock scanner asset lycaon/config/runtime/scanners/%s is missing", rel)
		}
	}
	for _, script := range r.Glob("scripts/*.sh") {
		for _, match := range scriptConfigPath.FindAllString(r.Text(script), -1) {
			if rel := "lycaon/" + strings.TrimSuffix(match, "/"); !exists(rel) {
				r.Report("%s names %s, which does not exist", script, rel)
			}
		}
	}
	scanners := "lycaon/" + config.ConfigDirName + "/" + config.ScannersDir.String()
	for _, f := range r.GoFiles(laws.AnyGo, "internal", "cmd", "pkg", "config", "test") {
		if strings.Contains(f.Rel, "/testdata/") || strings.Contains(f.Rel, "/rules/") {
			continue
		}
		for _, m := range goScannerAsset.FindAllStringSubmatch(string(f.Src), -1) {
			ref, trimmed := m[1], m[1]
			if i := strings.Index(ref, "scanners/"); i >= 0 {
				trimmed = ref[i+len("scanners/"):]
			} else if strings.Contains(ref, "/") {
				continue
			}
			if !exists(scanners + "/" + trimmed) {
				r.Report("%s names scanner asset %s, which is not under %s", f.Rel, ref, scanners)
			}
		}
	}
}

func manifests(r *laws.Run) map[string]workflowdef.Manifest {
	reg, err := workflowdef.RegistryFromDirs("")
	r.Must(err)
	all := reg.All()
	r.Expect(len(all), "bundled workflow manifests")
	return all
}

func productCatalog(r *laws.Run) []api.WorkflowSummary {
	rows, err := workflowcatalog.Resolver{}.ListResolved(context.Background(), "", "")
	r.Must(err)
	return rows
}

// The launch workflows are catalog-visible with their triggers; implement,
// which only session creation attaches, is not offered.
func launchWorkflowsAreInTheProductCatalog(r *laws.Run) {
	if m, ok := manifests(r)["bugbash@1.1.0"]; !ok {
		r.Report("bugbash@1.1.0 is not bundled")
	} else if s := m.Summary(); m.Topology != "bugbash" || !m.IsCatalogVisible() || s.Topology != "bugbash" || s.Trigger != "/bugbash" {
		r.Report("bugbash@1.1.0: topology %q, visible %t, summary %+v; want topology bugbash, visible, trigger /bugbash", m.Topology, m.IsCatalogVisible(), s)
	}
	want := map[string]string{"bugbash": "/bugbash", "options": "/options", "plan": ""}
	for _, row := range productCatalog(r) {
		if row.ID == "implement" {
			r.Report("implement attaches only at session creation and must not be in the product catalog")
		}
		if trigger, ok := want[row.ID]; ok {
			if trigger != "" && row.Trigger != trigger {
				r.Report("%s has trigger %q, want %q", row.ID, row.Trigger, trigger)
			}
			delete(want, row.ID)
		}
	}
	for _, id := range sortedKeys(want) {
		r.Report("the product catalog has no %s", id)
	}
}

// Each ambient workflow attaches one way, and implement@1.0.0 alone attaches
// at session creation.
func ambientAttachIsABijection(r *laws.Run) {
	catalog := manifests(r)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	r.Must(err)
	if err := workflowdef.ValidateAmbientAttachBijection(catalog, ref); err != nil {
		r.Report("%v", err)
	}
	for _, key := range sortedKeys(catalog) {
		m := catalog[key]
		if (m.Attach.Policy == workflowdef.AttachPolicySessionCreate) != (key == "implement@1.0.0") {
			r.Report("%s has attach policy %q; only implement@1.0.0 attaches at session creation", key, m.Attach.Policy)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
