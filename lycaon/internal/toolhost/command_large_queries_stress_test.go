//go:build stress

package toolhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStressLargeCommandQueries(t *testing.T) {
	f := newPerfFixture(t, nil)
	const count = 8192
	for i := range count {
		path := filepath.Join(f.Dir, "tree", fmt.Sprintf("pkg-%03d", i/64), fmt.Sprintf("file-%05d.go", i))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatalf("create source directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("line\n"), 0600); err != nil {
			t.Fatalf("write source: %v", err)
		}
	}
	cases := []string{
		`find tree -name '*.go' -print0 | xargs -0 -P 4 wc -l | awk '$2 != "total" {n += $1} END {print n}'`,
		`find tree -name '*.go' -exec wc -l {} + | awk '$2 != "total" {n += $1} END {print n}'`,
		`grep -r -c line tree | wc -l`,
	}
	for _, source := range cases {
		out, err := f.Call(t, context.Background(), source)
		if err != nil {
			t.Fatalf("large query: %v", err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("decode outcome: %v", err)
		}
		if result["state"] != "completed" || strings.TrimSpace(fmt.Sprint(result["stdout"])) != fmt.Sprint(count) {
			t.Fatalf("%s: %s", source, out)
		}
	}
}
