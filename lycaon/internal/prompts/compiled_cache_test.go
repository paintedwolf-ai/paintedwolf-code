package prompts

import (
	"fmt"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCompiledPromptSnapshotSeparatesSourcesAndExecutionData(t *testing.T) {
	engine := NewFileTemplateEngineLayers(PromptLayers{})
	testutil.FailErr(t, "register root", engine.Register("root.md", `{% include "child.md" %} {{ value }}`))
	testutil.FailErr(t, "register child", engine.Register("child.md", "first"))
	first, err := engine.Snapshot()
	testutil.FailErr(t, "snapshot first", err)
	if err := first.Register("child.md", "mutated"); err == nil {
		t.Fatal("immutable snapshot accepted registration")
	}
	for _, value := range []string{"one", "two"} {
		got, err := first.Render(t.Context(), "root.md", map[string]any{"value": value})
		testutil.FailErr(t, "render snapshot", err)
		if got != "first "+value {
			t.Fatalf("render = %q", got)
		}
	}
	testutil.FailErr(t, "replace child", engine.Register("child.md", "second"))
	second, err := engine.Snapshot()
	testutil.FailErr(t, "snapshot second", err)
	if first.Revision() == second.Revision() {
		t.Fatal("registered source change reused revision")
	}
	for _, c := range []struct {
		engine *FileTemplateEngine
		want   string
	}{{first, "first"}, {second, "second"}} {
		got, err := c.engine.Render(t.Context(), "child.md", nil)
		testutil.FailErr(t, "render child", err)
		if got != c.want {
			t.Fatalf("source snapshot changed: %q", got)
		}
	}
	testutil.FailErr(t, "replace with invalid graph", engine.Register("child.md", `{% include "root.md" %}`))
	cyclic, err := engine.Snapshot()
	testutil.FailErr(t, "snapshot cyclic source", err)
	if _, err := cyclic.Render(t.Context(), "root.md", nil); err == nil {
		t.Fatal("cached compilation bypassed cycle validation")
	}
}

func TestCompiledPromptConcurrentRenderDataIsIsolated(t *testing.T) {
	engine := NewFileTemplateEngineLayers(PromptLayers{})
	testutil.FailErr(t, "register template", engine.Register("parallel.md", "{{ value }}"))
	snapshot, err := engine.Snapshot()
	testutil.FailErr(t, "capture template snapshot", err)
	var workers sync.WaitGroup
	for i := range 24 {
		workers.Go(func() {
			want := fmt.Sprintf("request-%d", i)
			for range 3 {
				got, err := snapshot.Render(t.Context(), "parallel.md", map[string]any{"value": want})
				if err != nil {
					t.Errorf("render: %v", err)
					return
				}
				if got != want {
					t.Errorf("render data crossed requests: got %q, want %q", got, want)
					return
				}
			}
		})
	}
	workers.Wait()
}
