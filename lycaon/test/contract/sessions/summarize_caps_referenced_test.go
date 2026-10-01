package contract

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// summarizeCapExempt fields are validated or defined only in caps.go.
var summarizeCapExempt = map[string]string{
	"Version": "schema version checked in LoadCaps (caps.go only)",
}

// TestSummarizeCapsAllReferenced ensures every summarize.yaml cap leaf is read
// in production code.
func TestSummarizeCapsAllReferenced(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corpus := summarizeCapSourceCorpus(t, filepath.Join(root, "lycaon", "internal"))

	fields := summarizeCapLeafFields(reflect.TypeOf(summarize.Caps{}))
	dead := make([]string, 0)
	for _, name := range fields {
		if _, ok := summarizeCapExempt[name]; ok {
			continue
		}
		if strings.Contains(corpus, "."+name) {
			continue
		}
		dead = append(dead, name)
	}

	for _, name := range dead {
		t.Errorf("summarize cap field %q is defined but never read", name)
	}
}

func summarizeCapLeafFields(typ reflect.Type) []string {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if f.Type.Kind() == reflect.Struct {
			out = append(out, summarizeCapLeafFields(f.Type)...)
			continue
		}
		out = append(out, f.Name)
	}
	return out
}

func summarizeCapSourceCorpus(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasSuffix(path, string(filepath.Separator)+"caps.go") ||
			strings.Contains(path, string(filepath.Separator)+"summarize"+string(filepath.Separator)+"caps.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.Write(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("walk source: %v", err)
	}
	return b.String()
}
