package survey

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func parseFindResponse(t *testing.T, out string) findResponse {
	t.Helper()
	var resp findResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode find response: %v raw=%s", err, out)
	}
	return resp
}

func writeDeepFile(t *testing.T, root string, depth int, name string) string {
	t.Helper()
	parts := make([]string, 0, depth)
	for i := range depth - 1 {
		parts = append(parts, fmt.Sprintf("d%d", i))
	}
	dir := filepath.Join(append([]string{root}, parts...)...)
	testutil.FailErr(t, "mkdir deep", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write deep", os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
	return filepath.ToSlash(filepath.Join(append(parts, name)...))
}
