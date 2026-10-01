# Phase-3 admission timing runbook

This runbook is a step-by-step plan for the timing half of the v6.7
Phase-3 production-admission gate (`spec.campaign.v6.7`). It does not
execute the timing half. Run it only on a quiet host — never on a
loaded development box, where wall-clock numbers are not evidence.

See the companion correctness-half evaluation
(`trace.2026-07-20.tiller-worker-phase3-admission-eval` in Hyphae, or the
task report that shipped with this file) for the exact-trees, 206/206
curated-parity, and ParseState-replay results. Read that report first.
It found a large, deterministic allocation regression in the compact
route (`runner.parse`) versus production on all four canonical
fixtures. Fix that before you spend quiet-host time on wall-clock
measurement — a wall-clock win cannot satisfy an admission gate that
also requires zero allocation regression.

## Scope

Measure whether the compact/candidate materialization route beats the
current production `Parser.Parse` route by the Phase-3 admission
bar:

- at least 2% canonical equal-fixture geomean improvement;
- no canonical fixture more than 1% slower;
- no B/op, allocations/op, retained-heap, or peak-RSS regression on any
  fixture.

This runbook does not cover the 206-language fleet-refresh timing pass.
It covers only the four locked canonical fixtures.

## Fixtures (locked identities)

| Fixture | Bytes | Source SHA-256 |
|---|---:|---|
| `rewrite.go` | 5,116 | `74c0705f8729670559492fb5460a01b2a1a2a109928e1aeb52736e485e8ff097` |
| `query_compile.go` | 20,168 | `b788ee19b0075f0b9b567a9f93ea657e715bc8a6a40a99d3ca5c761404e71894` |
| `language.go` | 41,387 | `009aa9fd5352c712f3839670c7df8a9b00ae878ee20dc88131a438b2d5edfd9a` |
| `grammargen/lr.go` | 235,626 | `a7e4a1a64b25a60aea36183b9d6d53dcd9240942cdb10e67a3cf9e6ce30f95b2` |

Do not substitute fixtures. The driver in
`cgo_harness/pure_c/run_canonical_go_full_parse.sh` authenticates these
four against the hashes above and fails closed on drift.

## Host selection

Pick one of these two paths before you start. Record the choice and the
reason in a trace.

### Option A — fold into the pending enclave re-seal (recommended)

GopherCon freeze-checklist item 15 (owner decision, `plan.gophercon-freeze-checklist-2026-08`)
already schedules a sealed-enclave re-run at the frozen tag, using the
collector/verifier at `cgo_harness/cmd/go_c_timing_collect` and
`cgo_harness/cmd/go_c_timing_verify` (commit `183a2b69`). That image
rebuild is already budgeted (about $0.40) and already carries
Ed25519 receipt signatures plus RS256 JWKS attestation, so a Phase-3
production-vs-candidate pair rides for free alongside the run6-successor
receipt.

**Dependency, check first:** commit `183a2b69` lives on branch
`codex/c-timing-oracle-v3-source`, not on `main`. It has not merged.
Option A only works if that branch lands before the re-seal session, or
if the re-seal explicitly builds from that branch instead of `main`.
Confirm the merge status (or get an explicit citation decision from the
owner, per freeze-checklist item 15's "decide whether the verify tools
... merge to main") before committing to this path — do not assume the
collector/verifier is available on whatever ref the re-seal job checks
out by default.

Reasons to prefer this path:

- one hardware-attested run produces both the campaign-2 re-seal
  receipt and the Phase-3 candidate-admission receipt, instead of two
  separate quiet-host sessions;
- the collector already enforces artifact identity, environment hash,
  and atomic sample capture — exactly the rigor an admission gate
  needs;
- it avoids spinning up and validating a second quiet host under
  freeze-window time pressure.

Action: add `--go-backend candidate` (and, if the selected-store lane
is also a candidate for admission, `--go-backend selected-store`) to
the re-seal job's matrix, alongside the existing `--go-backend
production` run. Confirm with the owner before the image rebuild that
both backends are in scope for that session — do not silently expand
the re-seal job.

### Option B — plain n2d VM (fallback)

Use this only if the enclave re-seal is delayed past the point where
Phase-3 admission needs its own answer, or if the owner declines to
fold it in.

Provision:

- a dedicated GCE `n2d-standard-*` VM (no other tenants, no
  autoscaling group), or an equivalent dedicated quiet host;
- pin one physical core; disable SMT/hyperthread sibling scheduling for
  that core if the platform exposes the control;
- disable CPU frequency scaling (`cpupower frequency-set --governor
  performance` or the cloud-provider equivalent);
- confirm the VM has no cron jobs, package-manager timers, or
  monitoring agents that wake periodically;
- install the Docker engine and Go toolchain versions pinned in
  `cgo_harness/docker/Dockerfile` and `go.mod`.

## Command sequence

Run everything from a clean worktree at the exact revision under test.
Do not run the production and candidate passes on different revisions.

1. Clean worktree check:

   ```sh
   git status --porcelain   # must be empty
   git rev-parse HEAD       # record this SHA in the receipt
   ```

2. Production backend, publication defaults (four fixtures, five
   Go-C-C-Go cycles, ten samples per backend/fixture, `>=750ms` per
   sample, cgo/static-C deep-tree admission, bounded quiet-host
   admission):

   ```sh
   bash cgo_harness/pure_c/run_canonical_go_full_parse.sh \
     --go-backend production \
     --core <pinned-cpu> \
     --out <receipt-dir>/production
   ```

   The driver compiles this lane with `gts_no_parsercorephase0`, so the
   compact route is absent even when it is enabled in a normal build. The
   receipt records that tag as part of the workload identity.

3. Candidate backend, same revision, same pinned core:

   ```sh
   bash cgo_harness/pure_c/run_canonical_go_full_parse.sh \
     --go-backend candidate \
     --core <pinned-cpu> \
     --out <receipt-dir>/candidate
   ```

   The driver compiles this lane with `gts_parsercorephase0`. Keep both
   explicit tags intact: an untagged build is not an admissible production
   control now that the compact route is enabled by default.

   The script already interleaves Go and C samples in five ABBA cycles
   per backend run (order-balanced within a backend). Running
   production and candidate as two separate invocations, back to back
   on the same pinned core, is the accepted methodology this repo's
   receipts already use (see `BENCH.md`, "Exact-revision production and
   post-fusion compact-candidate receipt"). Do not interleave the two
   backend processes at the shell level — the script's own admission
   gates (quiet-host check, dirty-tree check, GOT_* env check) assume
   one backend per invocation.

4. If the selected-store lane is also in scope:

   ```sh
   bash cgo_harness/pure_c/run_canonical_go_full_parse.sh \
     --go-backend selected-store \
     --core <pinned-cpu> \
     --out <receipt-dir>/selected-store
   ```

5. Each invocation fails closed on: dirty worktree, inherited `GOT_*`
   overrides, non-quiet host, and a static-C or Go/candidate deep-tree
   digest mismatch against the locked hashes above. The quiet-host
   check requires three consecutive 10s-spaced PASS readings (up to
   twelve attempts) against all three of:

   - `load1 < 1.25` (a reading of `load1 >= 1.25` fails the attempt);
   - `io_avg10 <= 0.05`;
   - `MemAvailable >= max(MemTotal / 4, 8 GiB)`.

   The memory floor means an 8 GB host can never pass — `MemTotal / 4`
   never exceeds the 8 GiB floor on an 8 GB box, so `MemAvailable`
   would need to equal the entire machine's RAM with nothing else
   resident. Provision at least 16 GB RAM for either host option below
   (an `n2d-standard-2` at 8 GB is disqualified; use `n2d-standard-4`
   or larger). Do not pass `--allow-dirty`, `--allow-got-env`,
   `--skip-cgo-admission`, or `--skip-quiet-admission` on a publication
   run — publication mode rejects those flags outright (the script
   exits 2 before any sample is collected); they are recognized only
   under `--diagnostic`, where the receipt is labeled
   `NONPUBLICATION_DIAGNOSTIC` and cannot back an admission decision.

## GOMAXPROCS and pinning

The driver sets `GOMAXPROCS=1` internally for the timed samples; do not
override it. Pin the driver's own process group to the chosen core with
`--core`, not with an external `taskset` wrapper — the script's
`--core` selection is what it stamps into the receipt identity, and an
external wrapper the script cannot see will produce an unauthenticated
pin.

Reserve one additional CPU on the host for the collector process, the C
oracle build, and Docker's own overhead, so the timed process's core is
never scheduling-contended with a sibling task the script does not
control.

## A/B methodology

This is the same order-balancing already implemented by the driver, not
a new invention: each of the driver's five cycles alternates
Go-then-C-then-C-then-Go (a "Go-C-C-Go" ABBA cycle), which cancels
linear drift (thermal ramp, frequency-scaling settle) inside a single
backend's ten samples. Running production and candidate as two
back-to-back invocations on the same pinned core is a between-subjects
design across backends — acceptable here because:

- both backends measure against the same static-C oracle in the same
  session, so a same-host, same-oracle ratio is the actual comparison,
  not raw wall time;
- the repo's own accepted receipts already use this design (see
  `BENCH.md`).

If you want the higher-rigor within-subjects design (alternate
production and candidate samples directly, not just against C),
extend `run_canonical_go_full_parse.sh` to accept a list of backends
and interleave them inside its existing cycle loop, rather than hand-
rolling a second script. File that as a driver enhancement if the
between-subjects design's confidence interval is not tight enough to
resolve a 2% geomean claim.

## Statistical thresholds

Apply these checks in order. All five must pass for the timing half to
be a GO.

1. **Exact trees, first.** Before comparing any timing number, confirm
   both receipts report `AUTHENTICATED_CANDIDATE` (or the equivalent
   production label) with a deep-tree digest match against the locked
   hashes in the table above, and zero fallback
   (`candidate_fallback/op == 0` for all four fixtures, all samples). A
   receipt with any fallback or digest mismatch cannot feed the timing
   gate.

2. **Per-fixture regression floor (no fixture more than 1% slower).**
   For each fixture, compute the median ns/op ratio
   `candidate_median / production_median`. Fail the fixture if the
   ratio exceeds `1.01`. Use `benchstat` for the significance check
   alongside the raw ratio:

   ```sh
   benchstat -filter ".unit:ns/op" \
     <receipt-dir>/production/report.tsv \
     <receipt-dir>/candidate/report.tsv
   ```

   (The driver's `report.tsv` is not stock `go test -bench` output;
   convert it or run the parallel local-benchmark form —
   `BenchmarkGoParseWarmRealDFA` vs `BenchmarkParserCoreFreshFullCanonical`,
   both `-benchmem -count=10` — through `benchstat` directly if you
   need its native format. Cross-check both: the driver's medians are
   the publication numbers, the local Go-benchmark form is the fast
   pre-flight sanity check.)

3. **Canonical equal-fixture geomean (at least 2% improvement).**
   Compute the geometric mean of the four
   `production_median / candidate_median` ns/op ratios (note the
   direction — this is production-over-candidate, so a ratio above
   `1.0` is an improvement):

   ```
   geomean = (r_rewrite * r_query_compile * r_language * r_grammargen_lr) ** 0.25
   ```

   Require `geomean >= 1.0204`. A literal "at least 2% faster" means
   `candidate_time <= 0.98 * production_time`, so the equivalent
   production-over-candidate ratio floor is `1 / 0.98 = 1.020408...`,
   not `1.02` — `1.02` alone is only a ~1.96% floor and undershoots the
   spec's "at least 2%" wording. This mirrors exactly how every
   existing BENCH.md receipt reports its equal-fixture geomean (see
   "candidate improves the equal-fixture geomean by 20.07%" in the
   post-fusion receipt) — reuse that same arithmetic, do not invent a
   new geomean convention for this gate.

4. **Zero-tolerance alloc/RSS regression.** For each fixture, require:

   - `candidate_B_op <= production_B_op`;
   - `candidate_allocs_op <= production_allocs_op`;
   - `candidate_max_rss_kb <= production_max_rss_kb`.

   `go run ./cmd/benchgate` can express this as a hard gate on the
   local-benchmark form:

   ```sh
   go run ./cmd/benchgate \
     -base <production-bench-output> \
     -head <candidate-bench-output> \
     -benchmarks BenchmarkGoParseWarmRealDFA/rewrite,BenchmarkGoParseWarmRealDFA/query_compile,BenchmarkGoParseWarmRealDFA/language,BenchmarkGoParseWarmRealDFA/grammargen_lr \
     -max-bytes-regression 0.0 \
     -max-allocs-regression 0.0
   ```

   (Rename the `-benchmarks` list and swap `-base`/`-head` to match
   whichever local benchmark pair you ran; `benchgate` matches by
   benchmark name, so base and head must use the same names — run the
   candidate build under `BenchmarkParserCoreFreshFullCanonical` and
   temporarily alias it, or extend `benchgate` to accept a name-mapping
   flag. This is a real gap: today `benchgate` assumes base and head
   share benchmark names, which production (`BenchmarkGoParseWarmRealDFA`)
   and candidate (`BenchmarkParserCoreFreshFullCanonical`) do not. File
   this as a `benchgate` enhancement, or hand-check the four B/op and
   allocs/op pairs from raw `-bench -benchmem` output before trusting
   an automated gate here.)

   `-max-bytes-regression 0.0` and `-max-allocs-regression 0.0` do not
   mean zero-tolerance in absolute terms: `cmd/benchgate/main.go` adds
   a floor on top of the ratio (`minBytesOpFloor = 256`,
   `minAllocsOpFloor = 1`), so a fixture only fails if it regresses by
   more than +256 B/op or +1 alloc/op even at a `0.0` ratio. That floor
   exists to absorb CI noise on tiny benches; it is not the literal
   "no regression" the spec text asks for. Treat `benchgate` as a
   coarse smoke check only, and treat the hand-checked raw B/op and
   allocs/op pairs (production vs candidate, per fixture, from the raw
   `-bench -benchmem` output or the driver's `report.tsv`) as the
   authoritative strict gate: any increase at all, even one byte or one
   allocation, is a fail under the spec's literal wording.

   As of the correctness-half evaluation on `94a5439a` (2026-07-20, on
   a loaded box, so wall time is not cited — only the allocation
   counts, which are deterministic and load-independent), this check
   currently **fails** on every one of the four canonical fixtures:
   candidate allocs/op exceeds production allocs/op by 138x to 645x.
   B/op is worse on three of four fixtures and better only on
   `grammargen_lr`. Do not spend quiet-host time on step 2/3 above
   until this is fixed — it is a deterministic, revision-level fact,
   not a timing artifact a quiet host will change.

5. **Retained-heap check (operationalizing the Scope promise).** The
   driver already emits a retained-heap proxy per backend, under
   different column names because the two backends retain memory
   differently: the candidate/`selected-store` samples report
   `selected_retained_B_op` (the compact `SelectedStore`'s retained
   backing bytes per parse), and the production samples report
   `arena_B_op` (the production GLR arena's retained bytes per parse,
   which includes dead-alternative branches the forest never
   selects). Compare these two columns directly, per fixture, from
   `report.tsv`'s `go_median_selected_retained_B_op` (candidate run)
   against `go_median_arena_B_op` (production run). The two backends'
   `report.tsv` schemas differ in column count and order — the field
   is `go_median_arena_B_op` at column 14 in the production header and
   `go_median_selected_retained_B_op` at column 17 in the candidate
   header, so a single shared `cut` offset does not work:

   ```sh
   paste <(cut -f1,14 <receipt-dir>/production/report.tsv) \
         <(cut -f1,17 <receipt-dir>/candidate/report.tsv)
   ```

   Confirm both column indices against each file's own header row
   before trusting the `cut` offsets above — do not assume they stay
   aligned across driver versions; recompute the index from the header
   line every time you run this.

   Require `candidate_retained <= production_retained` per fixture.
   These two metrics are not measuring identical things (compact
   retained-store bytes versus production's full GLR arena, which
   over-counts relative to what survives selection), so treat a
   candidate win here as directional evidence for the "no retained
   heap regression" bullet in Scope, not as byte-for-byte proof the way
   the B/op/allocs/op check in step 4 is. If this check cannot be made
   apples-to-apples before a publication receipt is due, remove
   "retained-heap" from Scope's bullet list rather than publish a
   receipt that silently skips it.

## Recommendation

Fold this into the pending enclave re-seal (Option A) rather than
standing up a separate n2d VM, conditional on the owner confirming
scope before the image rebuild. Do not run any of this — enclave or
plain VM — until the allocation regression above is resolved or the
owner explicitly accepts the candidate route's current allocation
profile as a Phase-3 exception (which the spec's literal "no
allocations/op regression" wording does not appear to permit without a
spec amendment).

## Receipt requirements

Every timing receipt this runbook produces must record, per
`spec.campaign.v6.7`'s journaling rule:

- exact revision SHA;
- host identity (enclave attestation, or n2d VM instance ID + pinned
  core);
- the four fixture identities and their exact-tree digests;
- ten samples per backend per fixture, with the raw sample file cited
  by path or manifest hash;
- the geomean, per-fixture ratios, and alloc/RSS comparison computed
  per the thresholds above;
- a PASS/FAIL verdict against all five checks, not just the geomean.

Submit the receipt as a Hyphae spore against `spec.campaign.v6.7` and
update the roadmap's Phase-3 campaign entry with the gate's GO/NO-GO
disposition. Do not publish a partial receipt (for example, geomean
only, omitting the alloc check) as if it were a complete gate result.
