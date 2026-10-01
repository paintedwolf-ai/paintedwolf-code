package contract

import (
	"go/ast"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestSessionYAMLKeysMatchSessionLimitsStruct(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "host", "session.yaml"))
	contractcheck.FailErr(t, "read file", err)
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		contractcheck.FailErr(t, "unmarshal YAML document", err)
	}
	// These caps derive from true_window (ScaleLimitsFromTrueWindow /
	// ApplyLiveBudget), so bundled session.yaml omits them.
	derivedOnly := map[string]bool{
		"max_tool_result_bytes":       true,
		"max_coordinator_loop_cycles": true,
	}
	limType := reflect.TypeOf(settings.SessionLimits{})
	tagKeys := taggedYAMLKeys(limType)
	for key := range raw {
		if !tagKeys[key] {
			t.Fatalf("session.yaml key %q has no SessionLimits struct field", key)
		}
		if derivedOnly[key] {
			t.Fatalf("session.yaml must omit derived key %q", key)
		}
	}
	for key := range tagKeys {
		if derivedOnly[key] {
			continue
		}
		if _, ok := raw[key]; !ok {
			t.Fatalf("SessionLimits field %q missing from bundled session.yaml", key)
		}
	}
}

func TestNoSessionConfigTypeInSessionPackage(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sessionDir := filepath.Join(root, "lycaon", "internal", "session")
	fset, files := contractcheck.ParseNonTestGoTree(t, sessionDir)
	for _, f := range files {
		// The rule covers package session, not its subpackages.
		if f.Name.Name != "session" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != "Config" {
				return true
			}
			if ident.Obj != nil && ident.Obj.Kind == ast.Typ {
				t.Fatalf("session package must not define Config type in %s", fset.Position(ident.Pos()).Filename)
			}
			return true
		})
	}
}

func taggedYAMLKeys(t reflect.Type) map[string]bool {
	out := make(map[string]bool)
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		out[name] = true
	}
	return out
}
