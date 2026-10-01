package episode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCostLedgerIncludesUtilitiesUnknownCallsAndRetainedRollups(t *testing.T) {
	capture := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(capture, "store.db"))
	_, err := database.ExecContext(t.Context(), `INSERT INTO llm_calls(id,provider_id,model,caller,status,started_at,prompt_tokens,estimated_nano_usd,rate_snapshot,pricing_source,priced_as_of)
 VALUES ('live','cloud','candidate','coordinator','reported','2026-09-12T00:00:00Z',100,40000000,'{"input_per_1k":0.4,"currency":"USD"}','live','2026-09-12T00:00:00Z'),
 ('utility','cloud','helper','summarizer','reported','2026-09-12T00:00:00Z',10,5000000,'{}','live',NULL),
 ('uncertain','cloud','candidate','coordinator','started','2026-09-12T00:00:00Z',0,NULL,'{}','',NULL)`)
	testutil.FailErr(t, "seed cost receipts", err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO llm_call_rollups(provider_id,model,caller,day,call_count,prompt_tokens,estimated_nano_usd,priced_count,pricing_source)
 VALUES ('cloud','candidate','worker','2026-09-11',2,200,80000000,2,'live')`)
	testutil.FailErr(t, "seed cost rollup", err)
	if _, err := ReadCost(t.Context(), capture); err == nil {
		t.Fatal("uncheckpointed cost capture accepted")
	}
	_, err = database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint cost capture", err)
	before, err := os.ReadFile(filepath.Join(capture, "store.db"))
	testutil.FailErr(t, "read original capture", err)
	result, err := ReadCost(t.Context(), capture)
	testutil.FailErr(t, "read cost ledger", err)
	var count, nano int64
	var unknown, utility, rate bool
	for _, b := range result.Buckets {
		count += b.Calls
		if b.KnownNanoUSD != nil {
			nano += *b.KnownNanoUSD
		}
		unknown = unknown || (b.Status == "started" && b.KnownNanoUSD == nil && b.Calls == 1)
		utility = utility || (b.Model == "helper" && b.PromptTokens == 10)
		rate = rate || (b.Rate != nil && b.Rate.InputPer1K != nil && *b.Rate.InputPer1K == 0.4 && b.PricedAsOf == "2026-09-12T00:00:00Z")
	}
	if result.Version != 1 || count != 5 || nano != 125000000 || !unknown || !utility || !rate {
		t.Fatalf("cost ledger = %+v", result)
	}
	after, err := os.ReadFile(filepath.Join(capture, "store.db"))
	testutil.FailErr(t, "read observed capture", err)
	if string(before) != string(after) {
		t.Fatal("cost export changed the store")
	}
}

func TestCostLedgerDistinguishesMissingStoreAndEmptyStore(t *testing.T) {
	capture := t.TempDir()
	if _, err := ReadCost(t.Context(), capture); err == nil {
		t.Fatal("missing cost store accepted")
	}
	database := testdbfixture.OpenPath(t, filepath.Join(capture, "store.db"))
	_, err := database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint empty cost capture", err)
	result, err := ReadCost(t.Context(), capture)
	testutil.FailErr(t, "read empty cost ledger", err)
	if len(result.Buckets) != 0 {
		t.Fatalf("empty ledger = %+v", result)
	}
}

func TestCostLedgerKeepsUnpricedRollupsInMixedBuckets(t *testing.T) {
	capture := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(capture, "store.db"))
	_, err := database.ExecContext(t.Context(), `INSERT INTO llm_calls(id,provider_id,model,caller,status,started_at,estimated_nano_usd)
 VALUES ('priced','cloud','candidate','coordinator','reported','2026-09-12T00:00:00Z',1000)`)
	testutil.FailErr(t, "seed priced receipt", err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO llm_call_rollups(provider_id,model,caller,day,call_count)
 VALUES ('cloud','candidate','coordinator','2026-09-11',2)`)
	testutil.FailErr(t, "seed unknown rollup", err)
	_, err = database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	testutil.FailErr(t, "checkpoint mixed capture", err)
	result, err := ReadCost(t.Context(), capture)
	testutil.FailErr(t, "read mixed cost ledger", err)
	if len(result.Buckets) != 1 {
		t.Fatalf("cost buckets = %+v", result.Buckets)
	}
	bucket := result.Buckets[0]
	if bucket.Calls != 3 || bucket.UnpricedCalls != 2 || bucket.KnownNanoUSD == nil || *bucket.KnownNanoUSD != 1000 {
		t.Fatalf("mixed bucket = %+v", bucket)
	}
}
