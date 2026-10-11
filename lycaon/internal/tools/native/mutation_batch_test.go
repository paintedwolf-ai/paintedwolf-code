package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

type batchMutationTool interface {
	Run(context.Context, map[string]any, tools.ToolContext) (string, error)
}

func TestMutationBatchesRetainCommittedItemsOnLaterRefusal(t *testing.T) {
	cases := []struct {
		name, field, code string
		tool              func(*sandbox.Boundary) batchMutationTool
		args              map[string]any
	}{
		{"copy", "copied", "COPY_NOT_FOUND", func(b *sandbox.Boundary) batchMutationTool { return &CopyTool{Boundary: b} }, map[string]any{"copies": []any{map[string]any{"from": "a", "to": "b"}, map[string]any{"from": "missing", "to": "c"}}}},
		{"move", "moved", "MOVE_NOT_FOUND", func(b *sandbox.Boundary) batchMutationTool { return &MoveTool{Boundary: b} }, map[string]any{"moves": []any{map[string]any{"from": "a", "to": "b"}, map[string]any{"from": "missing", "to": "c"}}}},
		{"delete", "deleted", "DELETE_NOT_FOUND", func(b *sandbox.Boundary) batchMutationTool { return &DeleteTool{Boundary: b} }, map[string]any{"paths": []any{"a", "missing"}}},
		{"chmod", "results", "CHMOD_NOT_FOUND", func(b *sandbox.Boundary) batchMutationTool { return &ChmodTool{Boundary: b} }, map[string]any{"paths": []any{"a", "missing"}, "mode": "755"}},
		{"mkdir", "created", "MKDIR_FILE_EXISTS", func(b *sandbox.Boundary) batchMutationTool { return &MkdirTool{Boundary: b} }, map[string]any{"paths": []any{"new-dir", "a"}}},
	}
	if chownSupported() {
		cases = append(cases, struct {
			name, field, code string
			tool              func(*sandbox.Boundary) batchMutationTool
			args              map[string]any
		}{"chown", "results", "CHOWN_NOT_FOUND", func(b *sandbox.Boundary) batchMutationTool { return &ChownTool{Boundary: b} }, map[string]any{"paths": []any{"a", "missing"}, "owner": "current"}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			testutil.FailErr(t, "seed first target", os.WriteFile(filepath.Join(root, "a"), []byte("retained"), 0o644))
			output, err := tc.tool(nativefixture.Boundary(t)).Run(t.Context(), tc.args, nativefixture.Context(root))
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != tc.code {
				t.Fatalf("later item refusal = %v", err)
			}
			var receipt struct {
				BatchComplete bool                       `json:"batch_complete"`
				Completed     map[string]json.RawMessage `json:"completed"`
			}
			testutil.FailErr(t, "decode partial receipt", json.Unmarshal([]byte(output), &receipt))
			var items []json.RawMessage
			testutil.FailErr(t, "decode committed items", json.Unmarshal(receipt.Completed[tc.field], &items))
			if receipt.BatchComplete || len(items) != 1 {
				t.Fatalf("partial receipt = %s", output)
			}
			switch tc.name {
			case "copy", "move":
				got, readErr := os.ReadFile(filepath.Join(root, "b"))
				testutil.FailErr(t, "read committed destination", readErr)
				if string(got) != "retained" {
					t.Fatalf("destination = %q", got)
				}
			case "delete":
				if _, statErr := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(statErr) {
					t.Fatalf("deleted target state = %v", statErr)
				}
			case "chmod":
				info, statErr := os.Stat(filepath.Join(root, "a"))
				testutil.FailErr(t, "stat changed mode", statErr)
				if info.Mode().Perm() != 0o755 {
					t.Fatalf("mode = %o", info.Mode().Perm())
				}
			case "mkdir":
				info, statErr := os.Stat(filepath.Join(root, "new-dir"))
				testutil.FailErr(t, "stat created directory", statErr)
				if !info.IsDir() {
					t.Fatal("created target is not a directory")
				}
			case "chown":
				uid, gid, ownerErr := fileOwnership(filepath.Join(root, "a"))
				testutil.FailErr(t, "read owner", ownerErr)
				if uid != os.Getuid() || gid != os.Getgid() {
					t.Fatalf("owner = %d:%d", uid, gid)
				}
			}
		})
	}
}

func TestInterruptedMutationReceiptRetainsCancellation(t *testing.T) {
	output, err := interruptedMutationBatch(deleteResponse{Deleted: []string{"done"}}, context.Canceled)
	if !errors.Is(err, context.Canceled) || output == "" {
		t.Fatalf("interruption lost receipt or identity: %q %v", output, err)
	}
}

func TestEditMissCountsUnicodeCharacters(t *testing.T) {
	data := editMissData("file", "new text", "🚀éx")
	if data["old_string_chars"] != 3 {
		t.Fatalf("Unicode count = %v", data["old_string_chars"])
	}
}

func TestCopyByteBudgetCannotOverflowToEmptySuccess(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "a"), []byte("complete bytes"), 0o644))
	tool := &CopyTool{Boundary: nativefixture.Boundary(t)}
	output, err := tool.Run(t.Context(), map[string]any{
		"copies": []any{map[string]any{"from": "a", "to": "b"}}, "max_file_bytes": int64(1<<63 - 1),
	}, nativefixture.Context(root))
	testutil.FailErr(t, "copy with representable maximum", err)
	got, err := os.ReadFile(filepath.Join(root, "b"))
	testutil.FailErr(t, "read destination", err)
	if string(got) != "complete bytes" {
		t.Fatalf("copy silently truncated: %q (%s)", got, output)
	}
	for _, invalid := range []any{0, -1, 0.5, float64(1 << 63), "100"} {
		_, _, err := parseCopyPairs(map[string]any{"max_file_bytes": invalid})
		if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("budget %v accepted or misclassified: %v", invalid, err)
		}
	}
}
