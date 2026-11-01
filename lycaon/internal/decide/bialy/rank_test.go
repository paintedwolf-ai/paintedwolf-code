//go:build darwin || linux

package bialy_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRankReleasesDeviceBetweenBatches(t *testing.T) {
	for _, outcome := range []string{"success", "cancel", "invalid_scores", "engine_error"} {
		t.Run(outcome, func(t *testing.T) {
			cfg := fakeConfig(t)
			cfg.Resources = sharedResources(t)
			device, err := cfg.Resources.Devices.Bind(cfg.Resources.PhysicalDevice)
			testutil.FailErr(t, "bind generation", err)
			gate := filepath.Join(t.TempDir(), "gate")
			testutil.FailErr(t, "create response gate", unix.Mkfifo(gate, 0o600))
			fifo, err := os.OpenFile(gate, os.O_RDWR, 0)
			testutil.FailErr(t, "open response gate", err)
			t.Cleanup(func() { _ = fifo.Close() })
			second := `printf '{"id":%s,"scores":[2,3]}\n' "$id"`
			if outcome == "invalid_scores" {
				second = `printf '{"id":%s,"scores":[2]}\n' "$id"`
			} else if outcome == "engine_error" {
				second = `printf '{"id":%s,"error":"fixture failure"}\n' "$id"`
			}
			body := strings.Replace(fakeEngine, `"engine":{`, `"physical_device":{"kind":"pci","address":"0000:03:00.0"},"engine":{`, 1)
			body = strings.Replace(body, `    *'"method":"rank"'*)`, `    *'"candidates":["a","b"]'*) read -r release < '`+gate+`'; printf '{"id":%s,"scores":[0,1]}\n' "$id" ;;
    *'"candidates":["c","d"]'*) `+second+` ;;
    *'"candidates":["e"]'*) printf '{"id":%s,"scores":[4]}\n' "$id" ;;
    *'"method":"rank"'*)`, 1)
			testutil.FailErr(t, "install ranked fixture", os.WriteFile(cfg.Binary, []byte(body), 0o755))
			client := bialy.New(cfg)
			t.Cleanup(func() { _ = client.Close() })
			testutil.FailErr(t, "warm decision engine", client.Warm(t.Context()))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type result struct {
				scores []float64
				err    error
			}
			done := make(chan result, 1)
			go func() {
				scores, _, err := client.Rank(ctx, decide.HeadCodeRank, "task", []string{"a", "b", "c", "d", "e"})
				done <- result{scores, err}
			}()
			testutil.WaitFor(t, func() bool { return device.Snapshot().Active != nil })
			generation := make(chan *inference.Permit, 1)
			go func() {
				permit, err := device.Acquire(t.Context(), inference.Work{Owner: "generation", Generation: 1, Priority: inference.Foreground})
				if err == nil {
					generation <- permit
				}
			}()
			testutil.WaitFor(t, func() bool { return device.Snapshot().Queued == 1 })
			_, err = fifo.WriteString("release\n")
			testutil.FailErr(t, "complete first batch", err)
			permit := <-generation
			defer permit.Complete()
			testutil.WaitFor(t, func() bool { return device.Snapshot().Queued == 1 })
			select {
			case got := <-done:
				t.Fatalf("ranking finished before generation released its permit: %+v", got)
			default:
			}
			if outcome == "cancel" {
				cancel()
			}
			permit.Complete()
			got := <-done
			if outcome == "success" {
				testutil.FailErr(t, "rank all batches", got.err)
				if !reflect.DeepEqual(got.scores, []float64{0, 1, 2, 3, 4}) {
					t.Fatalf("scores = %v", got.scores)
				}
			} else {
				want := decide.ErrEngine
				if outcome == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(got.err, want) || got.scores != nil {
					t.Fatalf("failed batch returned scores %v, error %v", got.scores, got.err)
				}
			}
		})
	}
}
