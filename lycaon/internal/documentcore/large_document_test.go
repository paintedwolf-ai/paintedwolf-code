package documentcore

import (
	"strings"
	"testing"
	"time"
)

func TestLargeDocumentKeepsDifferentialUpdatesSmall(t *testing.T) {
	engine := coreForTest(t)
	callForTest(t, engine, Request{Action: "open", Handle: 1, Client: 1})
	text := strings.Repeat("a", 2<<20) + "🐺" + strings.Repeat("z", (2<<20)-5)
	started := time.Now()
	base := callForTest(t, engine, Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: text}}, Checkpoint: true})
	opened := time.Since(started)
	started = time.Now()
	edited := callForTest(t, engine, Request{Action: "edit", Handle: 1, Edits: []Edit{{Index: 2 << 20, Insert: "x"}}, CaptureUndo: true})
	if len(edited.Update) > 128 || len(edited.Undo) > 2048 || edited.Text != strings.Replace(text, "🐺", "x🐺", 1) {
		t.Fatalf("large edit failed: delta=%d undo=%d text=%d", len(edited.Update), len(edited.Undo), len(edited.Text))
	}
	t.Logf("4 MiB document: initial checkpoint=%d bytes in %s; one-character delta=%d bytes in %s; core memory=%d bytes", len(base.Checkpoint), opened, len(edited.Update), time.Since(started), engine.module.Memory().Size())
	undone := callForTest(t, engine, Request{Action: "undo", Handle: 1, Undo: edited.Undo})
	if undone.Text != text {
		t.Fatal("large-document selective undo changed neighboring Unicode")
	}
}
