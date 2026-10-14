package toolcommand

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

const bashParseTimeout = 50 * time.Millisecond

// treeSitterBashFileWrite detects heredoc/herestring file writes inside compound
// bash constructs (such as loops or conditionals) that cannot be flattened into
// a linear command sequence.
func treeSitterBashFileWrite(ctx context.Context, src, stdoutTo string) (path string, appendMode, matched bool) {
	if !strings.Contains(src, "<<") {
		return "", false, false
	}
	entry := grammars.DetectLanguageByName("bash")
	if entry == nil {
		return "", false, false
	}
	lang := entry.Language()
	tree, err := tsparse.ParseWithin(ctx, lang, []byte(src), bashParseTimeout)
	if tree != nil {
		defer tree.Release()
	}
	if err != nil || tree == nil || tree.RootNode() == nil {
		return "", false, false
	}
	srcBytes := []byte(src)
	var walk func(n *gotreesitter.Node) bool
	walk = func(n *gotreesitter.Node) bool {
		if n == nil {
			return false
		}
		if n.Type(lang) == "redirected_statement" || n.Type(lang) == "command" || n.Type(lang) == "pipeline" {
			if p, app, ok := inspectASTNodeFileWrite(n, lang, srcBytes, stdoutTo); ok {
				path, appendMode, matched = p, app, true
				return true
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			if walk(n.Child(i)) {
				return true
			}
		}
		return false
	}
	walk(tree.RootNode())
	return path, appendMode, matched
}

func inspectASTNodeFileWrite(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, stdoutTo string) (string, bool, bool) {
	hasHeredoc := false
	var targetPath string
	var appendMode bool
	var inspect func(cur *gotreesitter.Node)
	inspect = func(cur *gotreesitter.Node) {
		if cur == nil {
			return
		}
		switch cur.Type(lang) {
		case "heredoc_redirect", "herestring_redirect":
			hasHeredoc = true
		case "file_redirect":
			text := strings.TrimSpace(string(src[cur.StartByte():cur.EndByte()]))
			if p, app, ok := parseRawOutputRedirectText(text); ok && targetPath == "" {
				targetPath, appendMode = p, app
			}
		}
		for i := 0; i < int(cur.ChildCount()); i++ {
			inspect(cur.Child(i))
		}
	}
	inspect(n)
	if !hasHeredoc {
		return "", false, false
	}
	if targetPath == "" && stdoutTo != "" && stdoutTo != "/dev/null" {
		targetPath = stdoutTo
	}
	if targetPath == "" {
		return "", false, false
	}
	return targetPath, appendMode, true
}

func parseRawOutputRedirectText(text string) (string, bool, bool) {
	for _, prefix := range []struct {
		op     string
		append bool
	}{
		{"1>>", true}, {"&>>", true}, {">>", true},
		{">|", false}, {"&>", false}, {">&", false}, {"1>", false}, {">", false},
	} {
		if strings.HasPrefix(text, prefix.op) {
			rest := strings.TrimSpace(strings.TrimPrefix(text, prefix.op))
			w := parseBashWord(rest)
			if prefix.op == ">&" && isFileDescriptorTarget(w.value) {
				return "", false, false
			}
			if isStdoutFileTarget(w.value) {
				return w.value, prefix.append, true
			}
			return "", false, false
		}
	}
	return "", false, false
}
