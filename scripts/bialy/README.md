# Turn decisions: engine, data, and evaluation

The why and the map are in [`docs/decision-engine.md`](../../docs/decision-engine.md); this is the training loop.

At every visible user turn, and at the start of every worker leg, the host
asks a local decision model which units the request needs: which loadable
tool schemas join the call and which instruction units stay out of the prompt.
Skills are ranked when the coordinator requests one through free text. The questions live in
`lycaon/config/packs/painted-wolf/platform/host/decisions.yaml`; the host
side is `lycaon/internal/decide` (engine client), `lycaon/internal/promptunit`
(the unit catalog), and `lycaon/internal/coordinator/turnload` (the decision
and the ledger). This directory holds the engine daemon and the offline
tooling that trains and scores it. Everything here is a bespoke experiment:
it runs outside the verification queue and never during automated tests.

## What is decided, and what happens without an engine

| Unit | Decision | Without an answer |
|---|---|---|
| Loadable tool schema | one logical `multi` question, with each released tool option encoded independently; a tool loads when its P reaches `turn.tools.load_at` | only the floor is offered; `request_tools` loads the rest |
| Instruction unit (`shared/units/*.md`) | one `multi` question per turn, every unit an option; a unit is omitted when its P is below `omit_below` and its certainty reaches `confidence_floor` | every unit renders |
| Turn kind | one `choice` question; a confident `answer_only` vetoes tool loading | no veto |
| `skills_read` text | loaded skills ranked against the text; the best relevant skill is read | word overlap |
| `request_tools` text | loadable schemas ranked against the text | exact names and word overlap |

A joint turn head uses three engine rows: the state is encoded once per row, and
each option is a `[MASK]` marker the head reads on its own. A yes/no
question per option would put the state through the encoder once per option,
about 77 times per turn; the three-row set costs 0.25 s p50 on MLX. Option
texts are the tool name plus `option_words` words of its description,
exported into the corpus so training reads exactly what the host asks. `state.head_tokens` is the
engine's budget for the options; the request text gets what remains.

Each kind defaults to the behaviour the surface has without a model, and the
engine only moves it toward the failure the model can recover from: a
missing tool costs a `request_tools` round trip, an extra instruction costs
bytes. A unit attached to loadable tools follows them and is never scored.

Switched off (`LYCAON_DECIDE_DISABLED=1`), without a binary or checkpoint, or
past its deadline, the engine abstains and every site keeps the surface's own
default: the floor is offered, every loadable tool stays requestable behind
the capability map, every instruction unit renders, every skill lists, and
nothing is pre-read. That
is the only difference between off and on: on, the head adds likely tools to
the call and omits guides it is confident about; it never removes a tool the
floor offers, and the kind veto ships off (`kind.veto_tools`).

## Engine

The host launches Bialy (`bialy`,
`lycaon/internal/decide/native`) as a stdio subprocess speaking line-delimited JSON (`hello`,
`decide`, `rank`). One backbone stays resident and every request names the
head it wants; a head is a small set of weights swapped over the shared
encoder, so turn decisions and ranking share one memory footprint and one
forward path:

| Head | Trained by | Serves |
|---|---|---|
| `turn-load` | `scripts/bialy/train.py` | tool and unit decisions, turn kind |
| `unit-rank` | `scripts/bialy/train.py --families skills` | skill roster, `skills_read`, and `request_tools` ranking |
| `code-rank` | `scripts/bialy/rerank/train_rerank.py` | summarize, repomap, and project-search reranking |
| `web-rank` | web-page pairs | verified web pages and search snippets |

A head is a safetensors file whose header names the backbone it was trained
over (`scripts/bialy/headfile.py`); the engine refuses one trained over
another backbone, and a request for a head that did not load answers with the
checkpoint's own. The host resolves `bialy` beside its own executable and
the heads staged under `engine-root/decide/heads/<name>.safetensors`;
`LYCAON_DECIDE_HEADS="turn-load=PATH,unit-rank=PATH,code-rank=PATH"` names them outright
and `LYCAON_DECIDE_BINARY` / `LYCAON_DECIDE_MODEL_DIR` point a checkout at a
build and a checkpoint (`./task build:decide`, `pw decide ensure`). A packaged
app resolves the checkpoint `stage-engine.sh` bundles under
`engine-root/decide/models/`.
`LYCAON_DECIDE_DISABLED=1` switches the engine off; every decision then falls
back as the table above says.

On Apple silicon the engine runs the model on Apple's MLX (`--device mlx`,
what `auto` picks there): candle's Metal backend costs about five times more
per row, and the MLX port in `native/src/mlx.rs` mirrors the candle port op
for op, which its parity test checks on a tiny checkpoint. MLX's kernels
ship as `engine-root/decide/mlx.metallib` (about 136 MB, the framework's
whole kernel library); the host passes that path as `--metallib`, and the
engine also finds a copy beside its own executable. Building the engine on a
Mac compiles MLX from source once (CMake, network for the framework
checkout, and Xcode's Metal toolchain: `xcodebuild -downloadComponent
MetalToolchain` when the build says it cannot execute `metal`). The engine
caps MLX's allocator cache at 512 MB when it loads: every request has its own
batch size and sequence length, so an uncapped cache keeps growing toward the
GPU's working set (20 GB was measured on a rerank eval), and a resident
sidecar must not do that to the machine. With the cap the same eval holds
the engine at about 1.5 GB.

Heads are training artifacts. The shared release manifest,
`lycaon/config/packs/painted-wolf/platform/host/decision-release.json`, pins
their hashes, labels, backbone, and initial-preload option vocabulary. A
development checkout installs the matching set under
`<artifact bin>/decide-heads/<name>.safetensors`
(`python3 scripts/artifact_paths.py bin .` prints the directory; names are
`turn-load`, `unit-rank`, `code-rank`, `web-rank`), and every `build:lycaon-dev` stages
them into the engine root that `den:sidecar` and `den:app` run against.
Missing or mismatched required heads fail staging. The host intersects the
release vocabulary with permitted tools; new tools remain requestable without
changing the preloader's encoded options. Refresh the vocabulary and evaluate it
with its head before releasing it. The factory's `release_manifest.py` rebuilds
the manifest from the selected artifacts and their evaluated corpus.

Python is training-side only. `parity_probe.py --model ID --head FILE
--examples FILE --engine <launcher>` scores turns through the trainer's own
forward and through `bialy`. Report the actual probability gaps and
threshold crossings for the loaded heads; metadata compatibility alone does
not establish numerical parity, and agreement is not a model-quality test.
The training environment:

```bash
python3 -m venv .venv-decide && .venv-decide/bin/pip install laya
```

## Backbone

Laya ships two backbones with different head shapes, so a head only fits the
backbone it was trained on:

| `LYCAON_DECIDE_MODEL_ID` | Backbone | Hidden | Context |
|---|---|---|---|
| `convaiinnovations/laya` | ModernBERT-large, English vocabulary | 1024 | 512 |
| `convaiinnovations/laya-multilingual` | mmBERT-base, 100+ languages | 768 | 1024 |

The backbone is one choice for every head: the heads share the encoder, so
the replay eval here and the rerank eval must agree on it before either
ships. The shipped backbone is `convaiinnovations/laya-multilingual`: on the
same turn data it scored no worse than the English checkpoint and ran the
turn set in 0.81 s against 1.32 s p50 on candle Metal. Changing it means
training the turn-load head and running the replay eval under each id on the
held-out captures.

## Corpus

The host exports the corpus the decision scores; nothing here parses packs:

Artifacts live in the checkout's resolved build directory, never under the
checkout itself; every command below uses that directory as `$DECIDE`:

```bash
DECIDE="$(python3 scripts/artifact_paths.py build .)/decide"
BUILD_ONLY=true ./task eval:tool-usage   # builds lycaon-debug; or LYCAON_DEBUG_BINARY=... pointing at one
scripts/bialy/corpus.py --out "$DECIDE/corpus.json"
```

`corpus.json` carries every coordinator surface's floor, loadable tools, and
scored units; every tool's option text and request card; every unit and
skill card; the question templates; and the catalog revision that receipts
record. Third-party packs contribute units through the same catalog, so the
corpus is whatever the effective catalog resolves to.

## Data

Every decided turn leaves a receipt, including a turn whose engine was off or
abstained, and `lycaon-debug decide export` is the one reader that turns
receipts into training rows (`pw-decide-row/1`, described by
[`row.schema.json`](row.schema.json) and loaded through `rows.py`). A row is
the state the engine read, verbatim; the candidates the turn offered
(`offered`); what the engine answered (`engine`); and what the session then
did (`labels`). Coordinator turns and worker legs export alike: a worker
leg's receipt names its tool profile as the surface and its coordinator
session as `root_session`.

```bash
lycaon-debug decide export --db <store.db> --out rows.jsonl [--roots roots.txt]
```

Labels are always the host's own vocabulary and come from what the session
actually did: the offered loadable tools it called, the ones it asked for by
name, each `request_tools` need with the names it spelled out and the tools
it went on to call, the skills it read, and the kind those calls imply
(`turnload.ObservedKind`). A scored instruction unit attached to tools is
needed exactly when one of them was called; an unattached unit has no
behavioural label (`turnload.GuideLabels`). A turn that did not complete
keeps only its needs.

A tool a session called is weak evidence that the request needed it: driving
models call tools out of habit, and a head trained on calls learns which
tools are common rather than which a request needs. When judges have scored
the turn's loadable tools against its request (`labels.tool_scores`, and a
second judge's `labels.second_scores`), a tool is needed when both judges
scored it likely or certain, unneeded when both scored it at most unlikely,
and unlabelled otherwise, so the head never trains on a call two judges
disagree about; a tool the model asked for by name is always needed. A card
only one judge scored is unlabelled. Skill and need levels are the two
judges' mean, except that by default (`--rank-levels blended`) the skill a
turn read first and the tools used after a need train at the top level
whatever the judges scored them; `--rank-levels skills-blended` keeps the
override for skill reads only, and `--rank-levels judged` drops it. `--tool-truth` on the trainer,
calibration, and replay chooses the rule (`rows.TOOL_TRUTH`); rows without
tool scores fall back to the called tools.

A row whose `engine.state` is `answered` carried what the engine chose, so a
tool the engine preloaded and the session then called is not an independent
label: the turn families train on the rows the engine did not answer, and
under a live engine a turn's needs are exactly the tools it missed, which is
what the rank head ranks in production.

Open training data comes from the dataset factory, `bialy`: it
drives sessions with open-weights models in sandboxed runners against pinned
public repositories (through `lycaon-debug decide generate`, which drives a
JSON-lines task file through one sidecar and records a manifest), exports
their receipts, has an open-weights judge of another family score skill and
tool cards 0..4, and splits by repository and prompt group. Rows from your
own stores export the same way and train the same way; they stay on the
machine that exported them.

Generation tasks that select a workflow provide both `workflow` and
`workflow_version`. The runner verifies that exact catalog identity before
creating task sessions and records it in each result manifest; it does not
select a version implicitly. Omit both fields for ordinary chat tasks.
For example, `{"id":"review","prompt":"Review this change","workflow":"implement","workflow_version":"1.0.0"}` pins that definition.

## Train, calibrate, evaluate

Hold out whole packs and whole repositories: units from `--holdout-pack` stay
out of the guide labels so the eval measures generalization to units the
head never saw (what lets extensions ship their own units without
retraining), and the factory holds out every row of its held-out
repositories. The rest splits by prompt group, so the validation set is
prompts the head never trained on.

```bash
# turn-load answers the turn questions; unit-rank ranks skill and tool cards.
# The rank pairs are kept out of turn-load because they outnumber its rows and
# pull the shared weights. On a GPU host, set LYCAON_DECIDE_DEVICE=cuda.
scripts/bialy/train.py --corpus corpus.json --train train.jsonl --val val.jsonl --holdout-pack painted-wolf/browser \
  --families tools,guides,kind --tool-weight sqrt-inverse --seed 11 --out heads/turn-load.safetensors
scripts/bialy/train.py --corpus corpus.json --train train.jsonl --val val.jsonl \
  --families skills,requests --skill-scored 4 --skill-zeros 3 --out heads/unit-rank.safetensors

# Replay through the shipped engine, calibrate the thresholds, and time the turn set.
# The launcher execs: bialy serve --model <checkpoint dir> --model-id <id> --device mlx
#   --head-max-len <state.head_tokens> --head turn-load=<head> --head unit-rank=<head>
export LYCAON_DECIDE_ENGINE=<launcher>
scripts/bialy/replay_eval.py --corpus corpus.json --examples holdout.jsonl --holdout-pack painted-wolf/browser --json replay-holdout.json
scripts/bialy/calibrate.py --corpus corpus.json --examples val.jsonl
scripts/bialy/bench_latency.py --corpus corpus.json
```

The launcher runs the same binary with the same arguments the host uses, so
the replay measures the shipped engine; `parity_probe.py` cross-checks a
head against the trainer's own forward.

`train.py` precomputes the frozen backbone's features once and trains the
head for up to sixty epochs, stopping after twelve without a better
selection loss: the validation loss of tools and guides, the families that
change what a turn carries. Kind is trained and reported but does not pick
the checkpoint; its loss swings enough to stop a run before the tools head
has learned anything. The trainer reports precision and recall per family,
and per host for coordinator turns and worker legs, rather than one pooled
number, because the guide rows carry many always-true units that would hide
an unlearned tools head. Option sets come from each row's `offered`
candidates and option texts from the corpus, choice labels index options in
the engine's order (sorted by name), and skill pairs use the corpus's cards,
so the head sees at training exactly what it answers at the turn.
`--tool-weight sqrt-inverse` weighs each tool's positives by
sqrt(N / (n + 1)), clamped to [1, 20], so a tool few turns use still pulls
the head. Check a new head with the trainer's own forward before blaming the
engine: `bialy` matches it to the fourth decimal.

`replay_eval.py` reports tool load precision and recall (micro, and macro
over tools), loads per turn, guide omission precision and recall, kind
accuracy, skill top-1 and the share of turns whose first-read skill the
pruned roster listed, against judged skill scores the preload precision and
the share of turns whose roster lists a relevant skill, need ranking, bytes
saved per turn, the share of requests cut at `user_text_chars`, and engine
latency; overall and by host, surface, language, project, and the model that
drove the session, and for the held-out packs.
`calibrate.py` also reports the tool threshold each host would choose alone.
`bench_latency.py` measures the full turn question set on this machine,
which is the number the `deadline_ms` budget must respect.

A head ships when it beats the engine-off default on the held-out sets and
is at least as good as the shipped head on every set both can be scored on.
Off, no loadable tool is preloaded and every guide renders, so any tool
recall saves round trips, while a wrongly omitted guide costs the turn;
guide omission therefore turns on only when its precision on the held-out
repositories reaches 0.97. The rest are targets the shipped head reports
against: tool recall ≥ 0.9 (a missed tool costs a `request_tools` round
trip, so recall outranks precision), skill top-1 ≥ 0.7, first-read skill
listed ≥ 0.95, bytes saved ≥ 30% on investigate turns. On the generated
sessions a turn averages 11.8 coordinator calls of 7.5 s and 0.46
`request_tools` round trips, so the engine pays for itself once it avoids
about a quarter of them. The latency budget is the catalog's `deadline_ms`,
5 s for turn, request, lookup, and tool-event decisions: the decision runs once before
the turn's first model call, and one avoided round trip pays for many seconds
of it, so accuracy is the binding bar, not the engine's wall time.

## Author probe

`lycaon-debug decide probe --request "..." --surface implement_investigate`
runs the turn decision through the engine resolved from the environment and
prints what it would load and omit, so a pack author can see whether their
unit's description loads for the requests they intend.

## Shipped heads: B5 tools, B7G guides, E4 skills, open1 code

The release manifest pins four heads over the open1 dataset: B5 `turn-load`
for tools, B7G `guide-load` for guides, E4-dense1 `unit-rank` for skills and
needs, and open1 `code-rank`. The host asks the guides question of the guide
head when the release ships one and of `turn-load` otherwise.

B5 encodes each tool independently: consensus labels, 24 inverse-weighted
sampled negatives per turn, no rare-tool weighting or augmentation, learning
rate 5e-4, seed 11, batch 64, a 45-epoch cap. On validation it scores
0.822 precision and 0.291 recall at 0.95 with 0.308 preloads per turn; the
shipped cutoff is 0.6 (`decisions.yaml`), where 0.48 precision and 0.67
recall reach a needed tool on 76% of the turns that need one at 1.2 loads a
turn, because a wrong load costs one schema and a missed load costs a
round trip. Diagnostic requests outside
training show that initial prediction still misses useful tools; `request_tools`
covers the rest, with coverage 92.4% and top-1 77.7% at 2.59 loads per need
on the recorded validation check.

E4 uses two-judge labels and a seeded half-family augmentation mix. On 789
acceptance requests, 660 with a consensus-relevant skill, a relevant skill
was visible in the first six for 92.1%, and automatic preloads were relevant
in 122 of 123 cases; common-skill visibility is 87.9%, rare-skill 80.4%.

Stage the head set with `BIALY_HEADS_DIR` when building a checkout; the
build verifies it against the committed release manifest.
The committed release manifest binds its exact weights and option texts.

## Guide omission: B7

B7 trains the B5 recipe with `--families tools,guides`: every tool and every
guide option on its own row. Guide labels are relabelled from tool calls
before training: a unit was needed when its turn called a tool it `attaches`
to or one it is `needed_with`. `claim-evidence` and `survey-first-pass` carry
labels for the first time; earlier releases left them unknown, so no audit
could certify them.

The host only omits ids listed in `turn.guides.omittable`; an empty list
retains every guide. `train-host/guide_audit.py` certifies a unit at 97%
omission precision and 98% needed-guide retention on validation and on the
held-out repositories, and never from an unknown label. Keep that report with
the installed head and thresholds; replays enforce the same list.

Guides-only control (B7G, `--families guides`, 21 epochs, patience stop),
scored with `train-host/turn_probe.py` on the relabelled rows. Omission
precision (share of omitted turns that did not call the unit's tool) and the
share of turns omitted, on the held-out repositories, by `omit_below`:

| Unit | 0.15 | 0.20 | 0.25 | 0.30 |
|---|---|---|---|---|
| native-recall-tool | 98% / 94% | 98% / 96% | 98% / 97% | 98% / 98% |
| native-summarize-tool | 100% / 6% | 96% / 25% | 96% / 36% | 95% / 44% |
| native-find-tool | 100% / 0% | 97% / 5% | 94% / 9% | 94% / 14% |
| claim-evidence | 98% / 13% | 97% / 30% | 96% / 40% | 96% / 49% |
| native-list-dir-tool | 100% / 0% | 89% / 1% | 86% / 3% | 84% / 5% |

The other labelled units are never omitted below 0.30. The 97% bar certifies
only summarize and find at 0.15, where they are almost never omitted. The
shipped policy is 0.30 with those four units: every one holds 94% or better,
and a wrong omission ends at the next decision, since the host never omits a
unit once the chat has called or requested a tool it is needed by. Labels are observational: a unit whose tool is rarely called has
few positives, so retention swings on a handful of turns. `native-list-dir-tool`
stays out of the list; its 282 characters are not worth 86%.

## Tuning the shipped policy

Every threshold in `decisions.yaml` was set from evidence a new head changes.
Redo these steps for each head before touching a number, in this order, and
record the numbers beside the value in the catalog comment.

**Where the evidence is.** Every decision writes `turn_load_receipts` in the
dev store (`store.db`): `trigger` is `turn`, `request`, `lookup`, or
`tool_event`; `state_json` is what the engine read; `decisions_json` holds the
scores. Provider calls are in `debug/sessions/*/llm-requests.jsonl` with
`duration_ms`, `usage`, `completion.reasoning_bytes`, `tools[].Name`, and
`request_controls`. `lycaon-debug decide probe --request TEXT` scores one
request through the shipped heads; `scripts/bialy/request_probe.py` scores
`request_tools` needs against a surface's loadable tools and takes a
description override, so a schema edit can be tried before it is made.

**Tool preload, `turn.tools.load_at`.** Read the release's
`evidence/tool-validation.json`: precision, recall, loads per turn, and needed
turns reached at each cutoff. A wrong load costs one schema on the call; a
missed load costs a `request_tools` round trip, so choose the cutoff where the
next step down buys little recall. B5 sits at 0.6: 0.48 precision, 0.67
recall, 1.2 loads a turn, 76% of needing turns reached; 0.5 adds two points of
recall for 35% more wrong loads. Do not put editing tools on a posture floor to
make up for recall: with write on the first call the model designs the whole
program before it answers, and first turns went from 14 to 100 seconds.

**Guide omission, `turn.guides.omittable` and `omit_below`.** Score the
head's held-out rows with `train-host/turn_probe.py`, then tabulate omission
precision and share of turns omitted per unit at 0.15 to 0.30. List a unit
when it holds 94% or better and its omissions are worth the characters; a unit
without a behavioural label (no `attaches` or `needed_with`) never qualifies.
The host keeps a unit once the chat has called or requested a tool it is
needed by, so a wrong omission ends at the next decision. B7G ships at 0.30.

**Skill reads, `turn.skills.preload_at`, `lookup.read_at`,
`tool_event.read_at`, `pointer_at`, `margin`.** One read bar for all three
moments, one pointer bar, one margin. The margin is the precision guard: a
request that names its work scores its skill near 3 with the runner-up near 1,
and a close second means the text fits two procedures. Find misses in the
`turn` receipts where `preloaded_skill` is null and `skills.ranks[0]` clears the
bar with a lead; find wrong reads in the Local AI details. Shipped: read 2.8,
pointer 2.4, margin 0.3.

**Requests, `request.load_at`, `max_loads`, `nearest_loads`.** A miss shows in
a `request` receipt as `ranked` without the tool the model then asked for
again. The ranker reads the first 60 words of each description, so write the
operation first and the rejection codes after; probe the phrasings that missed
with `request_probe.py` before and after the edit. Tools that rank near one
another on the same need declare `companions` in `native-tools.yaml`, so one
selection loads the set. Lower the bar only when the probe shows the wanted
tool just under it and nothing wrong just above it. Shipped: 1.8, four loads,
no nearest fallback: in the recorded sessions the nearest tool was wrong both
times it loaded (`verify` for a file creation, `code_rewrite` for a render),
and the model's rephrase reached the right tool on the next call either way.

**Effort, `providers.yaml` `model_thinking`.** Send every host level
explicitly and round down when a model lacks one; never map medium up. Check
`request_controls.reasoning_effort` on the first coordinator call of a fresh
session, and `reasoning_bytes` against `duration_ms`: a first call over 30
seconds at low effort is a prompt problem, not a model one.

**Prompt weight.** The system prompt of a first build turn is the number to
watch, split by section: the capability map replaced an 18K-character tool
listing, the guide head removes about 2K more on an orientation prompt, and
the floor schemas are the rest. A retrain that improves preload recall is what
shrinks the request round trips; nothing in the catalog does.
