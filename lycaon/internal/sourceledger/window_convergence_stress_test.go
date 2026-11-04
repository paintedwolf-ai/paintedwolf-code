//go:build stress

package sourceledger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
)

// sampleOpenStacks is a wall-clock profile of the open: it samples the goroutine
// running OpenCommandWindow and tallies its host call path, leaf first.
func sampleOpenStacks(t *testing.T, files, open int) func() {
	done, finished := make(chan struct{}), make(chan struct{})
	tally := map[string]int{}
	total := 0
	go func() {
		defer close(finished)
		buf := make([]byte, 1<<22)
		for {
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
			n := runtime.Stack(buf, true)
			for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
				if !strings.Contains(stack, "OpenCommandWindow") {
					continue
				}
				var path []string
				for _, line := range strings.Split(stack, "\n") {
					if strings.Contains(line, "lycaon/internal/") && !strings.HasPrefix(line, "\t") {
						name := line[strings.LastIndex(line, "/")+1:]
						path = append(path, name[:strings.LastIndex(name, "(")])
					}
				}
				if len(path) > 7 {
					path = path[:7]
				}
				tally[strings.Join(path, " < ")]++
				total++
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		type row struct {
			path  string
			count int
		}
		rows := make([]row, 0, len(tally))
		for path, count := range tally {
			rows = append(rows, row{path, count})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
		for i, r := range rows {
			if i == 6 {
				break
			}
			t.Logf("PROFILE files=%d open=%d samples=%d/%d %s", files, open, r.count, total, r.path)
		}
	}
}

// Logs per-open time and outcome at the production budget on an untracked
// tree, with the Git engine on and complete watcher coverage.
func TestWindowOpensAtProductionBudget(t *testing.T) {
	gittestsetup.Enable()
	for _, files := range []int{1000, 5000} {
		t.Run(fmt.Sprintf("files-%d", files), func(t *testing.T) {
			sqlDB := testdbfixture.Open(t, "store.db")
			testdbseed.InsertProject(t, sqlDB, "p1")
			root := t.TempDir()
			for i := range files {
				dir := filepath.Join(root, fmt.Sprintf("pkg%d", i%50))
				if err := os.MkdirAll(dir, 0o750); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), fmt.Appendf(nil, "package p // %d\n", i), 0o640); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if _, err := sqlDB.ExecContext(t.Context(), `INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
				VALUES ('r1', 'p1', ?, 'root', 1, ?, 'attached')`, root, db.FormatTime(time.Now().UTC())); err != nil {
				t.Fatalf("insert root: %v", err)
			}
			store := New(sqlDB, t.TempDir())
			store.Commands.windowSettle = 0
			repochange.MarkCoverageCompleteForTest(root)
			t.Cleanup(func() { repochange.CloseRoot(root) })
			in := CommandWindowInput{ProjectID: "p1", Roots: []RootSpec{{ID: "r1", Path: root}}, SessionID: "s1", ToolName: "command", CommandLine: "make"}
			ctx := context.Background()
			for open := 1; open <= 25; open++ {
				started := time.Now()
				stop := sampleOpenStacks(t, files, open)
				window, err := store.Commands.OpenCommandWindow(ctx, in)
				elapsed := time.Since(started)
				stop()
				state, _ := store.Inventory.InventoryState(ctx, "p1", "", 0)
				t.Logf("MEASURE files=%d open=%d elapsed_ms=%d err=%v phase=%s", files, open, elapsed.Milliseconds(), err, state.Phase)
				if err != nil {
					continue
				}
				closeStart := time.Now()
				if err := window.Close(ctx); err != nil {
					t.Fatalf("close: %v", err)
				}
				<-window.Settled()
				t.Logf("MEASURE files=%d open=%d close_ms=%d", files, open, time.Since(closeStart).Milliseconds())
				if open > 1 && elapsed < 500*time.Millisecond {
					break
				}
			}
		})
	}
}
