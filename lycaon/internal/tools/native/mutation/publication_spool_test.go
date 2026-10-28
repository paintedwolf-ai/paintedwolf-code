package mutation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

type stagingQuota struct {
	used, maximum int64
	refuse        bool
}

func (q *stagingQuota) ReserveSpill(ctx context.Context, n int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.refuse {
		return nil, &toolcontract.ProgramMemoryLimitError{Account: "session_spill_bytes", Maximum: q.maximum, Observed: q.used + n}
	}
	q.used += n
	return sync.OnceFunc(func() { q.used -= n }), nil
}

func TestRedirectSpoolReleasesAdmissionOnSettlement(t *testing.T) {
	for _, action := range []string{"publish", "abandon", "limit", "quota", "cancel", "storage", "snapshot"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t, map[string]string{"out": "original"})
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 20, State: 1 << 20})
			testutil.FailErr(t, "program memory", err)
			h.owners.Revisions.Memory = memory
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			staged, err := h.publication().Open(ctx, h.destination(">", "out"))
			testutil.FailErr(t, "open redirect", err)
			defer func() { _ = staged.Abandon(context.Background()) }()
			_, err = staged.Write([]byte("first"))
			testutil.FailErr(t, "stage prefix", err)
			if h.quota.used == 0 {
				t.Fatal("staging acquired no spill admission")
			}
			switch action {
			case "publish":
				_, err = staged.Publish(ctx)
				testutil.FailErr(t, "publish", err)
				if h.read("out") != "first" {
					t.Fatal("incorrect published bytes")
				}
			case "limit":
				staged.(*fileStage).maximum = 5
				_, err = staged.Write([]byte("overflow"))
				if r := toolrejection.AsToolReject(err); r == nil || r.Code != "COMMAND_LIMIT_EXCEEDED" {
					t.Fatalf("limit = %v", err)
				}
			case "quota":
				h.quota.refuse = true
				_, err = staged.Write([]byte(strings.Repeat("x", 1<<20)))
				if r := toolrejection.AsToolReject(err); r == nil || r.Code != "COMMAND_LIMIT_EXCEEDED" {
					t.Fatalf("quota = %v", err)
				}
			case "storage", "snapshot":
				testutil.FailErr(t, "close staging storage", staged.(*fileStage).spool.Close())
				if action == "storage" {
					_, err = staged.Write([]byte("later"))
				} else {
					_, err = staged.Publish(ctx)
				}
				if err == nil {
					t.Fatal("closed staging storage succeeded")
				}
			case "cancel":
				cancel()
				_, err = staged.Write([]byte("later"))
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled write = %v", err)
				}
			}
			testutil.FailErr(t, "abandon settled redirect", staged.Abandon(context.Background()))
			if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
				t.Fatalf("retained staging memory = %+v", memory.InUse())
			}
			if h.quota.used != 0 {
				t.Fatalf("retained staging admission = %d", h.quota.used)
			}
			if action != "publish" && h.read("out") != "original" {
				t.Fatal("unpublished bytes changed destination")
			}
		})
	}
}

func TestRedirectMetadataAdmissionRefusesBeforeStorage(t *testing.T) {
	h := newHarness(t, map[string]string{"out": "original"})
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 20, State: 1})
	testutil.FailErr(t, "program memory", err)
	h.owners.Revisions.Memory = memory
	staged, err := h.publication().Open(t.Context(), h.destination(">", "out"))
	if staged != nil {
		_ = staged.Abandon(t.Context())
		t.Fatal("unadmitted metadata returned staging storage")
	}
	var limit *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &limit) || limit.Account != "state" {
		t.Fatalf("metadata admission = %v", err)
	}
	if h.quota.used != 0 || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) || h.read("out") != "original" {
		t.Fatal("refused metadata retained resources or changed destination")
	}
}
