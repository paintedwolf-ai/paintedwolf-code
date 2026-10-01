package compaction

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestFindPromptTailToolChunksSkipsBoundedReadPages(t *testing.T) {
	cfg := testCompactionConfig()
	readPage := fmt.Sprintf(`{"mode":"content","path":"game.py","content":%q,"total_lines":454,"truncated":true,"receipt":{"tool":"read"}}`,
		strings.Repeat("1|line of python code here\n", 400))
	msgs := []ContextMessage{
		{ID: "u1", Role: string(api.MessageRoleUser), Content: "run the game"},
		{ID: "a1", Role: string(api.MessageRoleAssistant), Content: "survey"},
		{ID: "t1", Role: string(api.MessageRoleTool), Content: readPage},
		{ID: "t2", Role: string(api.MessageRoleTool), Content: readPage},
		{ID: "t3", Role: string(api.MessageRoleTool), Content: readPage},
	}
	chunks := FindPromptTailToolChunks(msgs, cfg, nil)
	if len(chunks) != 0 {
		t.Fatalf("bounded read pages compacted: %+v", chunks)
	}
}
