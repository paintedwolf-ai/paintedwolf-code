package survey

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestDiscoveryKeepsScopeLikePathsLiteral(t *testing.T) {
	for _, path := range []string{"include_hidden", "src; max_depth=2", ".include_hidden=false,max_depth=2"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, path), 0o755); err != nil {
				t.Fatalf("create directory: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, path, "marker.txt"), []byte("needle"), 0o600); err != nil {
				t.Fatalf("create file: %v", err)
			}
			b := nativefixture.Boundary(t)
			for _, tool := range []interface {
				Name() string
				Run(context.Context, map[string]any, tools.ToolContext) (string, error)
			}{&ListDirTool{Boundary: b}, &FindTool{Boundary: b}, &GrepTool{Boundary: b}} {
				args := map[string]any{"path": path}
				if tool.Name() == "grep" {
					args["pattern"] = "needle"
				} else {
					args["max_depth"] = 1
				}
				before := maps.Clone(args)
				out, err := tool.Run(t.Context(), args, nativefixture.Context(root))
				if err != nil {
					t.Fatalf("%s literal path: %v", tool.Name(), err)
				}
				if !strings.Contains(out, "marker.txt") || !reflect.DeepEqual(args, before) {
					t.Fatalf("%s changed path scope: args=%v output=%s", tool.Name(), args, out)
				}
			}
		})
	}
}
