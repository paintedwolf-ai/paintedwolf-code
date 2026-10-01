package contract

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// routingContentSniffAllowlist exempts strings.Contains-on-content sites that do not
// select a compactor/preserver from arbitrary tool output. Key: fileBase:funcName.
var routingContentSniffAllowlist = map[string]string{
	"chunk_compactor.go:strategyFor":                            "idempotency — already-compacted chunks use truncate, not compactor routing",
	"overlay_promote_chunk.go:inspectOverlayPromoteToolContent": "promote output shape check for stamp/preserver; CompactChunk gates by diet_stamp/classifier",
}

var toolOutputRoutingScanFiles = []string{
	"lycaon/internal/llm/compaction/chunk_compactor.go",
	"lycaon/internal/llm/compaction/overlay_promote_chunk.go",
	"lycaon/internal/llm/compaction/tool_wire_compact.go",
}

var (
	contentSniffCallRE   = regexp.MustCompile(`strings\.Contains\(\s*(\w+)\s*,`)
	contentSniffFirstArg = regexp.MustCompile(`(?i)(content|output|payload|result|body|text|trimmed|line|jsonPart|banners)`)
	funcDeclRE           = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s+)?(\w+)\s*\(`)
)

// TestNoContentSubstringToolRouting forbids new content-substring routers in the
// compaction layer. Tool results that echo repo bytes must not be routed by sentinel
// strings they quote. See docs/dispatch-hints.md.
func TestNoContentSubstringToolRouting(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range toolOutputRoutingScanFiles {
		path := filepath.Join(root, rel)
		base := filepath.Base(rel)
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", rel, err)
		}
		currentFunc := ""
		sc := bufio.NewScanner(f)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if m := funcDeclRE.FindStringSubmatch(line); len(m) == 2 {
				currentFunc = m[1]
			}
			m := contentSniffCallRE.FindStringSubmatch(line)
			if len(m) != 2 || !contentSniffFirstArg.MatchString(m[1]) {
				continue
			}
			if !strings.Contains(line, `"`) {
				continue
			}
			key := base + ":" + currentFunc
			if reason, ok := routingContentSniffAllowlist[key]; ok {
				t.Logf("allowlisted content sniff %s:%d (%s)", key, lineNo, reason)
				continue
			}
			t.Errorf("%s:%d:%s — strings.Contains on %q selects by output content; route by tool identity instead", rel, lineNo, currentFunc, m[1])
		}
		if err := sc.Err(); err != nil {
			t.Fatalf("scan %s: %v", rel, err)
		}
		_ = f.Close()
	}
}

func TestToolOutputRoutingScanFilesExist(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range toolOutputRoutingScanFiles {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("missing routing scan file %s: %v", rel, err)
		}
	}
}
