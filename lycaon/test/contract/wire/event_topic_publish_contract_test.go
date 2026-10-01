package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const apiPackagePath = "github.com/lycaon/lycaon/pkg/api"

// EventHub.Publish and the durable outbox's enqueue calls take the topic as an argument.
var topicPublishMethods = map[string]bool{"Publish": true, "Enqueue": true, "EnqueueTx": true}

// TestEveryEventTopicHasPublishSite requires each declared topic to be passed to
// a publish or enqueue call in backend production code.
func TestEveryEventTopicHasPublishSite(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	topicByConst := eventTopicConstants(t, root)

	published := map[api.EventTopic]bool{}
	_, files := contractcheck.ParseNonTestGoTree(t, filepath.Join(root, "lycaon", "internal"))
	for _, file := range files {
		alias := contractcheck.ImportAliasFor(file, apiPackagePath)
		if alias == "" {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !topicPublishMethods[method.Sel.Name] {
				return true
			}
			for _, arg := range call.Args {
				sel, ok := arg.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == alias {
					if topic, ok := topicByConst[sel.Sel.Name]; ok {
						published[topic] = true
					}
				}
			}
			return true
		})
	}

	var missing []string
	for _, topic := range api.AllEventTopicValues() {
		if !published[topic] {
			missing = append(missing, string(topic))
		}
	}
	contractcheck.FailViolations(t, "event topics with no publish or enqueue call in lycaon/internal", missing)
}

// eventTopicConstants maps each generated EventTopic constant name to its value.
func eventTopicConstants(t *testing.T, root string) map[string]api.EventTopic {
	t.Helper()
	path := filepath.Join(root, "lycaon", "pkg", "api", "event_topic_ids.generated.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "parse "+path, err)
	constants := map[string]api.EventTopic{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 || !strings.HasPrefix(value.Names[0].Name, "EventTopic") {
				continue
			}
			lit, ok := value.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			topic, err := strconv.Unquote(lit.Value)
			contractcheck.FailErr(t, "unquote "+value.Names[0].Name, err)
			constants[value.Names[0].Name] = api.EventTopic(topic)
		}
	}
	if len(constants) != len(api.AllEventTopicValues()) {
		t.Fatalf("parsed %d EventTopic constants from %s, want %d", len(constants), path, len(api.AllEventTopicValues()))
	}
	return constants
}
