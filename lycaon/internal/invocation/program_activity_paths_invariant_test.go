package invocation

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// For every command the catalog offers, an execution recorded with resolved
// host paths presents exactly as one recorded with workspace paths: same rule,
// subject and step label.
func TestEveryCommandPresentsWorkspacePaths(t *testing.T) {
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		t.Fatal(err)
	}
	primary, docs := t.TempDir(), t.TempDir()
	roots := []projectroot.RootRef{{ID: "primary", Path: primary, IsPrimary: true}, {ID: "docs", Label: "docs", Path: docs}}
	display := func(path string) string { return projectroot.DisplayAbsolute(roots, path) }
	reads := [][2]string{{filepath.Join(primary, "src", "a.go"), "src/a.go"}, {filepath.Join(docs, "guide.md"), "@docs/guide.md"}}
	writes := [][2]string{{filepath.Join(primary, "out", "b.txt"), "out/b.txt"}, {filepath.Join(primary), "."}}
	recorded := func(name, family string, classes []string, pick int) activityInput {
		in := activityFixture(name, "root", name, name, 0)
		in.family, in.receipt.EffectClasses = family, classes
		if slices.Contains(classes, "mutation") {
			in.receipt.Lifecycle = "mutating"
		}
		read, write := []any{}, []any{}
		for _, path := range reads {
			read = append(read, path[pick])
		}
		for _, path := range writes {
			write = append(write, path[pick])
		}
		in.arguments["path_targets"] = map[string]any{"read": read, "write": write}
		in.arguments["path"] = reads[0][pick]
		return in
	}
	checked := 0
	for _, command := range catalog.Commands {
		for _, name := range append([]string{command.Name}, command.Aliases...) {
			for _, classes := range [][]string{{"read_only"}, {"mutation"}} {
				host, workspace := recorded(name, command.Family, classes, 0), recorded(name, command.Family, classes, 1)
				displayActivityPaths(host.arguments, display)
				if activityPresentation(host) != activityPresentation(workspace) || activitySubject(host) != activitySubject(workspace) {
					t.Fatalf("%s %v: host-recorded %q/%+v, workspace-recorded %q/%+v", name, classes,
						activitySubject(host), activityPresentation(host), activitySubject(workspace), activityPresentation(workspace))
				}
				g := activityGroup{input: host, counts: activityCounts{total: 1}}
				w := activityGroup{input: workspace, counts: activityCounts{total: 1}}
				if activityStepLabel(g) != activityStepLabel(w) {
					t.Fatalf("%s %v: step %q, want %q", name, classes, activityStepLabel(g), activityStepLabel(w))
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("catalog offered no commands")
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if display(outside) != outside {
		t.Fatalf("a path outside every root was rewritten to %q", display(outside))
	}
}

// For any program of redirected writes, with foreground work between them:
// every execution is in exactly one row, each producer shares its redirect's
// row, and each row a redirect anchors is named by every file its redirects wrote.
func TestRedirectedWritesAlwaysNameTheirFiles(t *testing.T) {
	pool := []string{"index.html", "style.css", "game.js", "notes/a b.txt", "index.html"}
	random := rand.New(rand.NewPCG(5, 6))
	for trial := range 500 {
		var groups []activityGroup
		statementOf := map[string]string{}
		add := func(input activityInput, node string) {
			groups = append(groups, activityGroup{input: input, key: activityKey{node: node, operation: input.operation}, parent: activityKey{node: "root"}, ordinal: int64(len(groups) + 1), counts: activityCounts{total: 1}})
		}
		for i := range 1 + random.IntN(6) {
			node := fmt.Sprintf("statement-%d", i)
			producer := producerFixture("producer-"+node, 0)
			if random.IntN(2) == 0 {
				producer = activityFixture("producer-"+node, "root", "cat", "cat", 0)
				producer.receipt.EffectClasses = []string{"mutation"}
			}
			operator := []string{">", ">>", "&>", "&>>"}[random.IntN(4)]
			write := redirectFixture("write-"+node, operator, pool[random.IntN(len(pool))], 0)
			add(producer, node)
			add(write, node)
			statementOf[producer.receipt.ID], statementOf[write.receipt.ID] = node, node
			if random.IntN(4) == 0 {
				listing := activityFixture(fmt.Sprintf("list-%d", i), "root", "list_dir", "ls", 0)
				add(listing, listing.receipt.ID)
			}
		}

		values := composeActivityGroups(groups, true)
		row := map[string]int{}
		for r, activity := range values {
			for _, id := range activity.ExecutionIDs {
				if previous, seen := row[id]; seen {
					t.Fatalf("trial %d: %s in rows %d and %d", trial, id, previous, r)
				}
				row[id] = r
			}
		}
		written := map[int][]string{}
		for _, g := range groups {
			id := g.input.receipt.ID
			r, ok := row[id]
			if !ok {
				t.Fatalf("trial %d: %s is in no row: %+v", trial, id, values)
			}
			if node := statementOf[id]; node != "" && r != row["write-"+node] {
				t.Fatalf("trial %d: %s left its statement's write: %+v", trial, id, values)
			}
			if path, ok := redirectDisplayPath(g.input.receipt); ok && !slices.Contains(written[r], path) {
				written[r] = append(written[r], path)
			}
		}
		for r, paths := range written {
			if values[r].Subject != boundedActivityText(pathSummary(paths)) {
				t.Fatalf("trial %d: row %d subject %q, wrote %v", trial, r, values[r].Subject, paths)
			}
		}
	}
}

func TestRedirectDisplayPathNeedsTheBoundOperator(t *testing.T) {
	for _, operator := range []string{">", ">>", "&>", "&>>"} {
		receipt := api.CommandExecutionReceipt{EffectMode: "publication", NativeCommand: operator, Command: operator + " notes/log 1.txt"}
		if got, ok := redirectDisplayPath(receipt); !ok || got != "notes/log 1.txt" {
			t.Fatalf("%s: %q %v", operator, got, ok)
		}
		receipt.EffectMode = "native"
		if _, ok := redirectDisplayPath(receipt); ok {
			t.Fatalf("%s outside a publication was read as a redirect", operator)
		}
	}
	if _, ok := redirectDisplayPath(api.CommandExecutionReceipt{EffectMode: "publication", NativeCommand: "write", Command: "write a.txt"}); ok {
		t.Fatal("a document publication was read as a redirect")
	}
}
