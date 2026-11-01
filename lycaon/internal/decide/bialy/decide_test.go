//go:build darwin || linux

package bialy_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/inference"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDecideReleasesDeviceBetweenWholeQuestionBatches(t *testing.T) {
	for _, outcome := range []string{"success", "cancel", "missing_answer"} {
		t.Run(outcome, func(t *testing.T) {
			answer := `"c":{"type":"noul","noul":0.3,"confidence":0.8}`
			if outcome == "missing_answer" {
				answer = ""
			}
			client, device, fifo := batchEngine(t, func(gate string) string {
				return strings.Replace(fakeEngine, `    *'"method":"decide"'*)`, `    *'"questions":{"a":'*'"b":'*) read -r release < '`+gate+`'; printf '{"id":%s,"answers":{"a":{"type":"noul","noul":0.1,"confidence":0.8},"b":{"type":"noul","noul":0.2,"confidence":0.8}}}\n' "$id" ;;
    *'"questions":{"c":'*) printf '{"id":%s,"answers":{`+answer+`}}\n' "$id" ;;
    *'"method":"decide"'*)`, 1)
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type result struct {
				value decide.Result
				err   error
			}
			done := make(chan result, 1)
			go func() {
				value, err := client.Decide(ctx, decide.HeadTurnLoad, "state", map[string]decide.Question{
					"c": decide.Noul("third?"), "b": decide.Noul("second?"), "a": decide.Noul("first?"),
				})
				done <- result{value, err}
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
			_, err := fifo.WriteString("release\n")
			testutil.FailErr(t, "complete decision batch", err)
			permit := <-generation
			defer permit.Complete()
			testutil.WaitFor(t, func() bool { return device.Snapshot().Queued == 1 })
			if outcome == "cancel" {
				cancel()
			}
			permit.Complete()
			got := <-done
			if outcome == "success" {
				testutil.FailErr(t, "decide all batches", got.err)
				if len(got.value.Answers) != 3 || got.value.Answers["a"].Noul != 0.1 || got.value.Answers["b"].Noul != 0.2 || got.value.Answers["c"].Noul != 0.3 {
					t.Fatalf("answers = %+v", got.value.Answers)
				}
			} else {
				want := decide.ErrEngine
				if outcome == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(got.err, want) || got.value.Answers != nil {
					t.Fatalf("failed batch returned %+v, error %v", got.value, got.err)
				}
			}
		})
	}
}
