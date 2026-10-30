package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEarlySearchExitLearnsWholeCapturedFile(t *testing.T) {
	for _, argv := range [][]string{
		{"grep", "-rl", "NeedleToken", "src"},
		{"grep", "-rq", "NeedleToken", "src"},
		{"grep", "-rm", "1", "NeedleToken", "src"},
		{"rg", "-l", "NeedleToken", "src"},
		{"rg", "-q", "NeedleToken", "src"},
	} {
		t.Run(fmt.Sprint(argv), func(t *testing.T) {
			content := "NeedleToken\n" + strings.Repeat("ordinary line\n", 10000) + "TailToken\n"
			h := newHarness(t, map[string]string{"src/captured.txt": content})
			h.owners.Catalog = sourcecatalog.New()
			cold := h.exec("", argv...)
			if cold.err != nil || cold.result.Status != 0 {
				t.Fatalf("early search=%+v", cold)
			}
			absent := h.exec("", "grep", "-rl", "DefinitelyAbsentToken", "src")
			absent.expect(t, "absent token", "", "", 1)
			info, err := os.Stat(filepath.Join(h.root, "src", "captured.txt"))
			testutil.FailErr(t, "captured source metadata", err)
			if fspath.ObserveFile(info).Known && len(h.pins.paths(absent.execution)) != 0 {
				t.Fatal("early matcher exit left a full-capture filter unlearned")
			}
			tail := h.exec("", "grep", "-rl", "TailToken", "src")
			tail.expect(t, "captured tail", "src/captured.txt\n", "", 0)
		})
	}
}
