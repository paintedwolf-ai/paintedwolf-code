package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestCodeRewriteMultiUsesOneCollaborativeTransaction(t *testing.T) {
	root := t.TempDir()
	docs := &fakeEditorDocuments{docs: map[string]*tools.EditorDocumentText{}, unsaved: true}
	for _, name := range []string{"a.go", "b.go"} {
		writeTreeFile(t, root, name, "package p\nfunc f() { fmt.Println(\"disk\") }\n")
		docs.docs["r1/"+name] = openDoc(name, "package p\nfunc f() { fmt.Println(\"draft\") }\n", true)
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	raw, err := tool.Run(t.Context(), map[string]any{"paths": []any{"."}, "recursive": true, "pattern": `fmt.Println($A)`, "rewrite": `log.Info($A)`}, editorCtx(root, docs))
	testutil.FailErr(t, "apply collaborative refactor", err)
	if len(docs.batches) != 1 || len(docs.batches[0]) != 2 {
		t.Fatalf("transaction calls = %+v", docs.batches)
	}
	var result multiApplyOut
	testutil.FailErr(t, "decode refactor result", json.Unmarshal([]byte(raw), &result))
	if len(result.Changed) != 2 {
		t.Fatalf("refactor result = %+v", result)
	}
	for _, file := range result.Changed {
		if !file.InEditor || !file.Unsaved {
			t.Fatalf("publication incorrectly reported: %+v", file)
		}
		text, err := os.ReadFile(filepath.Join(root, file.Path))
		testutil.FailErr(t, "read unchanged disk", err)
		if strings.Contains(string(text), "log.Info") {
			t.Fatalf("bypassed document owner for %s", file.Path)
		}
		if !strings.Contains(docs.docs["r1/"+file.Path].Text, `log.Info("draft")`) {
			t.Fatalf("missed accepted draft in %s", file.Path)
		}
	}
}
