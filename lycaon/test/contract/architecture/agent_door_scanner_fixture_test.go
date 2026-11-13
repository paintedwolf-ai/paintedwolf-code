package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The door scanners catch each shape a bypass can take: a direct call, a
// function value, a package-level closure, an aliased or dot import, and a
// client or dialer built in place. Clean code produces nothing.
func TestDoorScannersCatchEveryUseShape(t *testing.T) {
	parse := func(src string) *ast.File {
		t.Helper()
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
		if err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		return file
	}

	effects := parse(`package fixture
import (
	run "os/exec"
	"net"
	"net/http"
)
var start = run.Command
var hidden = func() { _, _ = net.Dial("tcp", "example.test:80") }
func direct() { _ = run.CommandContext(nil, "sh") }
func client() { _ = http.DefaultClient; _ = &http.Client{}; _ = net.Dialer{} }
`)
	got := strings.Join(agentEffectUses(effects), "\n")
	for _, want := range []string{"uses run.Command", "uses net.Dial", "uses run.CommandContext", "uses http.DefaultClient", "builds http.Client", "builds net.Dialer"} {
		if !strings.Contains(got, want) {
			t.Errorf("effect scanner missed %q in:\n%s", want, got)
		}
	}
	if uses := agentEffectUses(parse(`package fixture
import . "os/exec"
func f() { _ = Command("sh") }
`)); len(uses) != 1 || !strings.HasPrefix(uses[0], "dot-imports") {
		t.Errorf("dot import: %v", uses)
	}

	files := parse(`package fixture
import fs "os"
var open = fs.Open
var remove = func(p string) error { return fs.Remove(p) }
func write() { _ = fs.WriteFile("a", nil, 0o600) }
`)
	var keys []string
	for _, use := range agentFilesystemUses(files, "x.go") {
		keys = append(keys, use.key+" "+use.call)
	}
	joined := strings.Join(keys, "\n")
	for _, want := range []string{"x.go:<package> Open", "x.go:<package> Remove", "x.go:write WriteFile"} {
		if !strings.Contains(joined, want) {
			t.Errorf("filesystem scanner missed %q in:\n%s", want, joined)
		}
	}

	clean := parse(`package fixture
import "os"
func ok() { _ = os.Getenv("HOME") }
`)
	if uses := agentEffectUses(clean); len(uses) != 0 {
		t.Errorf("clean effects: %v", uses)
	}
	if uses := agentFilesystemUses(clean, "c.go"); len(uses) != 0 {
		t.Errorf("clean filesystem: %v", uses)
	}
}
