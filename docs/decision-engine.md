# Decision engine

The host runs Bialy, our tuned local decision engine, and asks it typed questions: once
per human request, before that request's first provider call, which loadable
tool schemas join the call and which instruction units stay out of the prompt;
and, at retrieval sites, how lexical candidates
rerank against the task. Every answer is a calibrated probability the host
consumes as a fact, never a heuristic over prose ([Architecture](architecture.md#typed-decisions-from-a-local-model)).
This page is the why and the map; [Coordination](coordination.md) covers the
turn, and `scripts/bialy/README.md` the training loop.

Bialy builds on [Laya](https://github.com/NandhaKishorM/laya)
and its multilingual checkpoint, with decision heads tuned for Painted Wolf Code.
We credit the Laya authors for the upstream model and training implementation,
and [laya-candle](https://github.com/Trystan-SA/laya-candle) for the Rust port
our native runtime derives from. See [Dependencies](dependencies.md#decision-model)
and [Laya notices](../licensing/texts/laya-multilingual-notices.md) for provenance and licensing.

## Which runs decide

Every coordinator request and worker assignment uses local-model turn
optimization. Workflows declare their phases, tools, and required instructions;
the host selects optional additions and omissions within that surface. There is
no workflow-specific enablement flag. Model-invoked retrieval also uses the
engine: `request_tools` and `skills_read` resolve their text through it, and
rerank sites such as `summarize` blend its scores.

`LYCAON_DECIDE_DISABLED=1` disables the engine for troubleshooting. An absent,
disabled, or failed engine leaves applicable guidance in place and tools
requestable. The decision never suppresses required workflow instructions.

A decision serves a human request, not a transcript row. The request's first
turn that reaches a model call decides, with the request's own text: a user
turn with its instruction, a worker leg with its brief, or, when the host
opened that turn, the request the run resolved. A workflow started from a slash
command and a user turn the host parked before any model call therefore decide
on the wake that first prompts the model. Later host turns of the same request
keep its standing surface.

## What it may and may not do

The engine chooses only among what a surface already permits. It never widens
a surface, changes a permission floor, or answers an approval. Each decision
errs toward the failure the model can recover from: a tool loads with
confidence and is otherwise requestable through `request_tools`; the call
names only the capabilities that can still load, never the tools; an instruction
unit is omitted with confidence only when its id is listed in
`turn.guides.omittable` for the installed head, and otherwise renders. An empty
list keeps all guidance. Calibration must report label coverage per unit; an
unknown label never establishes that a unit is safe to omit. Below a threshold,
past the deadline, or without an engine, a decision abstains and the turn
runs as it would with no model. The answer-only veto exists in the catalog
and ships off; an independent head answers no kind question at all.

The tools and guides questions share one encoding. An independent head reads
every tool and every guide option on its own row, so a unit's answer never
depends on the rest of the roster and a pack can add units without moving
another unit's score. A unit's label comes from the tools its turn called:
the tools it `attaches` to, or the tools it declares itself `needed_with`
when it gates nothing (the evidence rules are needed by a turn that captured
a page; the read ladder by a turn that oriented itself). A unit that names
neither has no behavioural label and is never omitted.

## Skills: preload, lookup, and the first tool call

Three moments select a skill, all through the same ranking of the profile's
effective catalog and the same rule: the top skill is taken when its score
reaches the moment's bar and leads the runner-up by `margin`. A close second
means the text fits two procedures, and reading one of them would steer the
turn on a coin flip.

- **Turn start.** The catalog is ranked against the request; the top skill is
  read at `turn.skills.preload_at` (2.8). Zero disables the step.
- **`skills_read`.** The model's free-text need is ranked; the top skill is
  read at `lookup.read_at` (2.8). An exact skill name bypasses ranking.
  Unanswered lookups return a bounded discovery catalog for the model to
  choose from.
- **The first loadable tool call.** Once per turn, when the turn first calls
  a loadable tool and no skill is read yet, the catalog is ranked against the
  request and that tool with its bounded description. The tool is the
  strongest structured signal the host has about the work under way: a page
  capture names visual verification, a commit names the commit procedure,
  where the request text alone did not. The top skill is read at
  `tool_event.read_at` (2.8) and named in one line at `tool_event.pointer_at`
  (2.4), so a fit the engine is less sure of costs one line rather than a
  body. Editing tools are skipped (`tool_event.skip`): their names pull skills
  about writing rather than about the work.

The selected body is saved with the turn's standing state and supplied on
subsequent provider calls, including recovery; a pointer is saved the same
way. A new turn clears both. Engine and rendering failures leave no skill.
Both render at one fixed point of the prompt, so a read that lands mid-turn
shifts the cached prefix once and holds for the rest of the turn.
The Local AI details identify the skill and what selected it. The full skill
list stays out of context. Tool preloading uses B5 at 0.6; requested tools
keep their own ranking threshold and result limit.

## Discovery without ranking

`request_tools` and `skills_read` keep exact-name access independent of Bialy.
A request containing only one exact tool name bypasses ranking; an unambiguous
skill identifier does too. Described needs use the local ranker. When it is
unavailable, disabled, late, or faulted, the result includes `discovery` with
`status: ranking_unavailable` and a structured `failure`. A completed ranking
that selects nothing retains its normal rejection without a catalog. Discovery
never loads a tool or reads a skill body. A mixed tool request can load its
explicit names and return discovery for the unanswered portion.

Each page contains up to 20 available names and bounded descriptions, in name
order. The model selects an entry by retrying its exact name. Passing
`discovery.next_need` unchanged as `need` retrieves another fallback page without
ranking. These continuation values and selection instructions appear only in
fallback results; standing prompts and tool schemas carry no discovery overhead.
Cursors bind to the caller, request, and catalog. A changed catalog or invalid
continuation requires repeating the original description. Tool catalogs contain
only live, requestable entries on the current surface; skill catalogs use the
caller's effective skills. Resource reads require an exact skill and cannot browse.
An empty catalog offers no new capability; existing tools remain usable.

Selection budgets are 10 seconds for turn decisions, automatic skill ranking,
tool requests, and skill lookups. Enabled retrieval rerank sites have their own
deadlines in `decisions.yaml`. Caller cancellation still ends the operation; it
does not trigger discovery. Startup separately warms the engine with its
90-second handshake budget.

## Release vocabulary and retrieval

The decision release owns the initial-preload option names and texts as well as
its weights. The host intersects this vocabulary with the surface's permitted
loadable tools. New tools remain available through `request_tools`, whose ranker
reads the current cards; they do not change a released preloader's inputs until
its vocabulary is refreshed and evaluated. Editing an existing tool description
also leaves the release's encoded option text intact. Receipts record the
candidates actually scored. The released tool head encodes each option separately,
so adding or removing another permitted option does not change its representation.
Joint heads require a matching joint release and retain their roster sensitivity.

## The standing surface and the prompt cache

The tools a chat offers beyond its floor and the units it leaves out are part
of the prefix a provider caches, so the host changes them only when the change
is needed or costs nothing ([Providers § Prompt cache policy](providers.md#prompt-cache-policy)).
Each decision opens warm or cold, read from recorded facts: it is cold on the
chat's first decided request, on a route whose provider keeps nothing between
requests, after an idle gap at least as long as the prefix's lifetime, on a
different provider or model than the chat's last call, and when a local runner
no longer holds the model. History compaction does not make the standing tier
cold: its separate checkpoint can still be reused. The history epoch stays in
the receipt as provenance, independently of cache warmth. A cold boundary
inside a request is read at the next request's decision; mid-request the model
is working with the surface it has, and replacing it would pull tools out from
under that work.

A cold turn's decision replaces the standing tools and omissions. A warm turn
keeps the standing tools and adds its own predictions at the same threshold,
since a schema a turn predicts often serves a later one, and an omission
holds only while every turn since the last cold boundary agrees with it, so
a unit shown to be needed comes back. `request_tools` adds to the set on any
turn. An abstained decision keeps the standing tools and renders every unit.
When a worker assignment cannot be resolved, the host keeps applicable
guidance and explicitly requested tools without inferring a task from host prose.
Missing route policy or call history does not establish a cold cache. Tools
that become unavailable on the active surface leave immediately, and the
final schema list has canonical name order.

Every decision leaves a receipt: the state the engine read, the answers, the
catalog revision, the cache boundary the turn opened on, and the standing
surface afterwards, which a restart restores. A `request_tools` receipt records
the ranker output separately from the final activation, after surface filtering;
only that activation changes the standing surface. Receipts reach Den on the
transcript page and the `turn_load` event. They stay in the local store;
developer tooling can export a machine's own receipts as training examples,
and the app never sends them off the device ([Privacy](privacy.md#the-local-decision-model)).

## The pieces

| Piece | Where |
|---|---|
| Policy: questions, thresholds, deadlines, rerank sites | `lycaon/config/packs/painted-wolf/platform/host/decisions.yaml` |
| Engine process (`bialy`, Rust, candle everywhere, MLX on Apple silicon) | `lycaon/internal/decide/native` |
| Host client, checkpoint pin, provisioning, preflight | `lycaon/internal/decide/bialy` |
| Turn decisions, cache boundaries, and the standing-surface ledger | `lycaon/internal/coordinator/turnload` |
| Reranking sites | `lycaon/internal/decide/rerank.go` and each site's blend |
| Heads (training artifacts) | `engine-root/decide/heads/<name>.safetensors`, pinned by `lycaon/config/packs/painted-wolf/platform/host/decision-release.json`: `turn-load` (tools), `guide-load` (guides, when shipped), `unit-rank`, `code-rank` |

The packaged app ships the checkpoint, the heads, and MLX's Metal library
under its engine root; a development checkout builds the engine with
`./task build:decide`, provisions the checkpoint at the sidecar's first boot,
and stages the checksum-pinned head set installed under the shared artifact cache
([Dev tasks](dev-tasks.md#targets-with-behavior-the-description-cannot-carry)).
Artifacts, pins, and the runtime choice are in [Dependencies](dependencies.md#decision-model).

Bialy retains the `pw-decide-row/1`, `pw-decide-head/1`, and
`pw-decide-anchor/1` artifact identifiers so existing training data, trained
heads, and release checksums remain valid. These identify saved formats, not
the engine or repository name.

## Retraining

Heads are retrained, the backbone is not. The loop, with commands, lives in
`scripts/bialy/README.md` (turn decisions) and `scripts/bialy/rerank/README.md`
(reranking):

1. **Export rows from receipts.** Every decided turn, coordinator or worker,
   leaves a receipt with the state the engine read and the candidates the
   turn offered, even when the engine was off. `lycaon-debug decide export`
   turns receipts into `pw-decide-row/1` training rows labeled by what the
   session then called, read, and needed; a turn that did not complete keeps
   only its `request_tools` needs. A turn the engine answered carried the
   engine's own choices, so its tool labels train only the rank head.
2. **Generate and judge open data.** The dataset factory
   (`bialy`) drives requests written for pinned public
   repositories through sandboxed sidecars with open-weights models
   (`lycaon-debug decide generate`), exports their rows, and has an
   open-weights judge of another family score every skill card and every
   need's tool cards 0..4, because behaviour alone never labels most of
   either catalog. It holds out whole repositories and splits the rest by
   prompt group. Prompt diversity, not session count, is what the head
   learns from.
3. **Train.** `train.py` runs over the frozen backbone's features and writes
   a head file whose header names its backbone; the engine refuses a head
   trained over another. The turn questions train `turn-load` and the skill
   and need pairs train `unit-rank`; one head for both lets the more
   numerous rank pairs pull the turn answers.
4. **Replay through the shipped engine.** `replay_eval.py` scores the head
   through `bialy` on the held-out sets; `calibrate.py` picks
   thresholds against the bars, reports the bars no threshold meets, and
   prints the `decisions.yaml` fragment;
   `bench_latency.py` measures the turn set; `parity_probe.py` checks a head
   against the trainer's own forward before the engine is blamed.
5. **Ship.** A new head ships when it is at least as good as the shipped one
   on the agreed release tasks, with trade-offs reported on common evaluation
   sets. Keep tool preloading enabled for the current release. Install the whole
   selected head set under the artifact cache and record its labels, backbone
   and checksums in `lycaon/config/packs/painted-wolf/platform/host/decision-release.json`. The build rejects partial or
   different sets. Preserve the input rows, trainer commit, calibration, raw
   predictions and runtime comparison with the release artifacts. Heads are open weights under
   Apache-2.0; heads trained on the factory's open dataset can ship with it.

The bars, the shipped heads' data, results, and known limits are in
[`scripts/bialy/README.md`](../scripts/bialy/README.md#shipped-heads-b5-tools-b7g-guides-e4-skills-open1-code),
next to the scripts that produced them, and
[Tuning the shipped policy](../scripts/bialy/README.md#tuning-the-shipped-policy)
says how each threshold was set and how to set it again for a new head.
