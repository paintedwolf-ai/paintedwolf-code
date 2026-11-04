package toolhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandWideReviewCompletesWithoutManualBatching(t *testing.T) {
	for _, count := range []int{100, 13000} {
		f := newPerfFixture(t, nil)
		name := strings.Repeat("a", 60) + ".go"
		if err := os.WriteFile(filepath.Join(f.Dir, name), []byte("hello\n"), 0600); err != nil {
			t.Fatalf("write source: %v", err)
		}
		if err := os.WriteFile(filepath.Join(f.Dir, "args.txt"), []byte(strings.Repeat(name+"\n", count)), 0600); err != nil {
			t.Fatalf("write arguments: %v", err)
		}
		out, err := f.Call(t, context.Background(), "xargs wc -l < args.txt")
		if err != nil {
			t.Fatalf("wide review: %v", err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("decode outcome: %v", err)
		}
		if result["state"] != "completed" {
			t.Fatalf("%d operands: %s", count, out)
		}
	}
}
