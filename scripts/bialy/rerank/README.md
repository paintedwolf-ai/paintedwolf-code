# Reranking with the decision engine: data, training, evaluation

The host ranks candidates against a task at several seams: the files and
definitions a `summarize` pack admits, the symbols on a repomap page, the hits
Den project search returns, the pages web research verifies. Each seam keeps
its own lexical order and, when its site is enabled in
`lycaon/config/packs/painted-wolf/platform/host/decisions.yaml`, blends in the
decision engine's relevance for the lexical top K. The seam is
`lycaon/internal/decide` (`Reranker`); the engine is `bialy`
(`lycaon/internal/decide/native`), the Bialy sidecar, and `lycaon/internal/decide/bialy` is its client.

This directory holds the offline tooling that answers one question per site:
does the engine beat the lexical order, by how much, and at what latency.
Everything here is a bespoke experiment: it runs outside the verification
queue and never during automated tests. Training needs Python; serving does
not.

## Engine

```bash
./task build:decide                      # <build dir>/bialy, Metal on Apple silicon
bialy check --model <checkpoint dir> --head code-rank=heads/code-rank.safetensors
bialy bench --model <checkpoint dir> --candidates 16
```

The checkpoint directory is a Laya checkpoint (`rl_agent_config.json`,
`model.safetensors`, `encoder/config.json`, `tokenizer/`); the host provisions
the pinned one with `pw decide ensure`, and a Hugging Face cache snapshot has
the same layout. One backbone stays resident and every request names its head
(`turn-load`, `code-rank`, `web-rank`); a head that did not load answers with
the checkpoint's own. The engine refuses a head trained over another backbone.

The evaluation command runs the host's own client, so it needs the same
environment the host would resolve:

```bash
export LYCAON_DECIDE_BINARY="$(python3 scripts/artifact_paths.py build "$PWD")/bialy"
export LYCAON_DECIDE_MODEL_DIR=~/.config/paintedwolf-dev/decide-models/convaiinnovations--laya-multilingual@e4e9ddf21a7b
export LYCAON_DECIDE_HEADS="code-rank=$PWD/.task/decide/heads/code-rank.safetensors"
```

## Corpus and pairs

`decide-rerank` is the evaluation command. Build it once into the ignored
task directory:

```bash
go build -o .task/decide/bin/decide-rerank ./lycaon/cmd/decide-rerank
```

Harvest definitions with the host's own parsers, then write pairs. A pair is
a request whose answer is one unit. Pairs come from the dataset factory
(`bialy coderank`, in bialy), which keeps each unit's leading
comment as a free, human-written request and has pinned open-weights models
write requests across a language and register panel, so every training
request is reproducible from open models. A doc-derived request is the
candidate's own text once the candidate carries its leading comment, so only
the model-written requests measure anything on doc-bearing text.

```bash
B=.task/decide/bin/decide-rerank; D=.task/decide/data
$B harvest --repo lycaon --name lycaon --include internal --out $D/units-lycaon.jsonl
# In bialy: bialy coderank pairs --units <units dir> --out <pairs dir>
scripts/bialy/rerank/synthesize_sites.py code --units $D/units-lycaon.jsonl --pairs $D/pairs/model-lycaon.jsonl \
   --repo lycaon --out $D/rows-sites-lycaon.jsonl
```

`synthesize_sites.py` writes rows for the site shapes the harvest cannot
produce (search hits, neighbours, imports, next actions, web pages) with
explicit rubric labels. Held-out repositories never contribute training rows.

## Evaluate a site

`eval` drives the site through its real entry point (the summarize engine,
`repomap.Build`, the live code leg) with the engine attached, and reports the
target's rank under the site's lexical order and under the blend:

```bash
$B eval --site summarize_definitions --repo ../ast-grep --name ast-grep \
   --units $D/units-ast-grep.jsonl --pairs $D/pairs/model-ast-grep.jsonl --k 16 --chunk 16 --deadline 8s \
   --json .task/decide/eval/definitions-ast-grep.json --dump $D/dump-definitions-ast-grep.jsonl
```

`--no-engine` runs the lexical order only and still writes the dump, which is
how training rows are produced without spending engine time. `--weight`,
`--k`, `--chunk`, and `--deadline` (a duration) override the catalog policy
for a sweep; measure with a generous deadline, because a call the engine loses
to the deadline counts as unchanged. Run one engine at a time on a laptop GPU:
two engines sharing it both miss their deadlines.

The report gives MRR, nDCG@10, and hit@1/5/10 for lexical and blended, how
many pairs improved or regressed, abstention reasons, and p50/p95 latency of
the engine call. `web_pages` has no offline corpus and is measured live.

## Train

```bash
scripts/bialy/rerank/train_rerank.py --dump $D/dump-definitions-lycaon.jsonl --dump $D/rows-sites-lycaon.jsonl \
   --model convaiinnovations/laya-multilingual --out .task/decide/heads/code-rank.safetensors
scripts/bialy/headfile.py show .task/decide/heads/code-rank.safetensors
```

Labels are the site's own structure on the engine's 0..4 rubric: the target
4, another unit in its file 2, a lexical neighbour from another file 1, a
random candidate 0; synthetic site rows carry explicit levels. The rubric
text is imported from the engine's serving code so training and serving
cannot drift. The head file is safetensors with the backbone, label, and
validation metrics in its header (`scripts/bialy/headfile.py`); a GPU host
trains one in minutes (`--batch-size 64` on an H100), a laptop in hours.

The shipped `code-rank` head learns from requests written by various models
over code units from open-source repositories. It is open weights under
Apache-2.0, published without its training pairs.

## Results

Measured 2026-09-25 on an Apple M1 Pro. Held-out repositories never
contributed training rows: ast-grep (Rust) and aws-vault (Go). Questions are
model-written requests describing a unit's purpose without naming it (`claude`
pairs); doc-derived questions cannot measure doc-bearing candidate text, because
the request is then the candidate's own comment (lexical MRR 0.94). MRR is the
mean of 1/rank of the right unit; "+/−" counts questions the engine moved up or
down; latency is p50 of one engine call through the host's own client with an
8 s deadline so no call abstains.

**Candidate text matters more than the head.** Adding the signature line
raised the lexical baseline on ast-grep definitions from MRR 0.134 to 0.193,
and the leading comment raised it again; both ship regardless of the engine.
**Zero-shot base models lose to lexical order on every site**; never run the
base model without a head.

**Round three heads** (every site shape, the multi-language corpus, and
model-written questions in training; multilingual 151k examples, English
151k examples, both on an H100). The multilingual head runs through
`bialy`; the English head through the Python runtime, which is two to
three times faster per candidate than the native engine on this GPU:

| head, K | definitions ast-grep n=95 | definitions aws-vault n=54 | repomap ast-grep n=88 | repomap aws-vault n=51 | p50 |
|---|---|---|---|---|---|
| multilingual r3, K=16 | 0.096 → 0.120, +16/−7 | 0.135 → 0.163, +9/−2 | 0.100 → 0.125, +11/−4 | 0.121 → 0.133, +5/−2 | 1.1 s |
| multilingual r3, K=48 | 0.096 → 0.132, +30/−13 | 0.135 → 0.169, +23/−8 | 0.100 → 0.133, +25/−8 | 0.121 → 0.157, +15/−5 | 3.5 s |
| English r3, K=16 | 0.096 → 0.120, +17/−4 | 0.135 → 0.151, +4/−0 | 0.100 → 0.106, +12/−3 | 0.121 → 0.110, +3/−3 | 0.85 s |
| English r3, K=48 | 0.108 → 0.139, +27/−17 | 0.132 → 0.153, +13/−9 | 0.100 → 0.137, +26/−6 | 0.121 → 0.136, +14/−9 | 2.5 s |

hit@10 moves the same way: multilingual K=48 lifts definitions ast-grep from
0.263 to 0.368 and repomap ast-grep from 0.227 to 0.341.

**The native engine reproduces the Python runtime exactly**: the same head
through `bialy` and through torch gives identical MRR and identical
improved/regressed counts (multilingual r3 on MLX, definitions ast-grep:
0.096 → 0.132, +30/−13 at K=48 and 0.120, +16/−7 at K=16, the table's rows). Only the
latency differs: 3.6 s against 1.0 s at K=48, 1.1 s against 0.3 s at K=16,
because candle's Metal matmul tops out near 2 TFLOPS on this GPU where torch
reaches five to six.

**Project search is hurt by the engine** (file-level target): ast-grep
0.777 → 0.623, aws-vault 0.665 → 0.541 with the multilingual r2 head; the
leg's own score is already strong and narrow, so any blend reorders it. The
site stays wired and off. **Structure ranking is already solved by lexical
order** when the comment is in the file head (hit@1 0.95–1.00).

**Verdict.** Reranking helps where lexical order is weak (requests that do not
share words with the unit): +0.02 to +0.04 MRR at K=48, +0.01 to +0.03 at
K=16, on definitions and repomap pages; it is neutral where lexical order
already finds the words and harmful on project search. The shipped policy is
the multilingual checkpoint (half the cost of English, 100+ languages, the
larger K=48 gains) at K=48 in chunks of 16 with a 2.5 s deadline: on the MLX
runtime the engine ships with on Apple silicon, definitions on ast-grep at
K=48 measure 0.096 → 0.132 MRR, +30/−13, at 1.0 s p50 / 1.35 s p95 per call
with no deadline abstentions and a 1.5 GB engine footprint, where candle on
Metal needed 3.5 s for the same gain. The unmeasured sites (windows,
neighbors, call sites, imports, next actions, web pages) stay off until they
have evaluation pairs; project search stays off because the engine hurts it.
