# Trace formats — identification, field maps, aggregations

## Identify by shape

Query the top-level keys first (native `jq` with query `keys` for files under 20 MiB; otherwise print `sorted(d)` from the script), then the container's keys. Match the first row that fits.

| Shape you see | Format | Producer |
|---|---|---|
| `{version, recording:{records, sampleStackTraces, sampleDurations}}` | Safari Web Inspector timeline | Safari / WKWebView Timelines tab → Export |
| `{traceEvents:[…]}` or a bare array of `{ph, ts, name}` | Trace Event Format | Chrome DevTools Performance, Perfetto JSON export, `chrome://tracing`, many tracers |
| `{nodes:[{callFrame, hitCount, children}], samples, timeDeltas}` | V8 `.cpuprofile` | DevTools CPU profile, `node --cpu-prof`, `v8-profiler` |
| `{meta:{interval}, threads:[{stackTable, frameTable, funcTable}]}` | Gecko / Firefox Profiler | Firefox Profiler export (often `.json.gz`) |
| `{shared:{frames}, profiles:[{unit, samples, weights}]}` | speedscope | speedscope, and many exporters targeting it |
| gzip magic `1f 8b`, not JSON after `gunzip` | pprof protobuf | Go `pprof`, many non-JS runtimes |
| protobuf, `.perfetto-trace` | Perfetto | Perfetto / Android systrace |
| Text lines beginning `tick,` / `code-creation,` | V8 `--prof` log | `node --prof` |

Extensions are worthless here: Safari, Chrome, cpuprofile, Gecko, and speedscope files are all `.json`.

Non-JSON formats are not worth hand-parsing. Use `go tool pprof -top -nodecount=40` or `-traces` for pprof (declare `go` in `capability_request.host_resources`), `trace_processor_shell` with SQL for Perfetto, and `node --prof-process` for a V8 log (declare `node`).

---

## Safari Web Inspector timeline

Times are **seconds**, relative to a page-load epoch. Wall clock is `recording.endTime - recording.startTime`.

| Path | Holds |
|---|---|
| `recording.records[]` | `{type, startTime, endTime}` or `{type, timestamp}`; `type` is `timeline-record-type-{script,layout,rendering-frame,screenshots,network,cpu,heap-allocations,media}` |
| `.eventType` on script records | `timer-fired`, `event-dispatched`, `microtask-dispatched`, `animation-frame-fired`, `observer-callback`, `script-evaluated`, and the zero-width `*-installed` / `*-requested` / `*-canceled` bookkeeping kinds |
| `.eventType` on layout records | `composite`, `recalculate-styles`, `invalidate-styles`, `layout`, `forced-layout`, `invalidate-layout`, `paint` |
| `recording.sampleStackTraces[]` | `{timestamp, stackFrames:[{sourceID, name, line, column, url}]}` — **leaf-first** |
| `recording.sampleDurations[]` | Parallel weights, seconds, index-matched to `sampleStackTraces` |
| `recording.markers[]` | `{time, type, details}`; `console.timeStamp("label")` arrives as `type: "timestamp"` |
| `recording.memoryPressureEvents[]` | System memory-pressure notifications during the capture |
| screenshot records | `imageData` as a `data:image/png;base64,…` URI — never print one |
| network records | HAR-shaped `entry` with `request`, `response`, `timings` |

**Containers:** `rendering-frame` (one per frame, tiles the whole timeline) and layout `composite` (commit→present, swallows idle and the JS that ran inside it). Real main-thread cost is script + layout with `composite` and `rendering-frame` excluded.

`forced-layout` against `layout` is a free honest signal — a high ratio means synchronous layout thrash. So is the `recalculate-styles` tail; look at p99 and worst, not the mean.

In a Tauri app the WKWebView is served from `http://127.0.0.1:<port>`, so these recordings are the real shipping shell, not a browser harness. Native calls appear as `ipc://` network entries.

### Self and total time from the sample stacks

```python
# save as @scratch/trace_agg.py and run with the trace path as the argument
import json, collections, sys
PATH = sys.argv[1]
d = json.load(open(PATH))
r = d["recording"]
stacks, weights = r["sampleStackTraces"], r["sampleDurations"]
total_sampled = sum(weights)

def key(f):
    return f"{f.get('name') or '(anon)'} @{f.get('url','').rsplit('/',1)[-1]}:{f.get('line')}"

self_t, total_t = collections.Counter(), collections.Counter()
for s, w in zip(stacks, weights):
    frames = s["stackFrames"]
    if not frames:
        continue
    self_t[key(frames[0])] += w                 # leaf-first: index 0 is the leaf
    for k in {key(f) for f in frames}:          # set() so recursion counts once
        total_t[k] += w

print(f"{len(weights)} samples, {total_sampled:.2f}s sampled")
for k, v in self_t.most_common(15):
    print(f"  self {v*1000:7.1f}ms {100*v/total_sampled:5.1f}%  {k}")
```

Confirm the orientation before trusting it: the *last* frames should be entry points (`eventHandler`, `promiseReactionJob`, a flush or dispatch function), not application leaves.

### Record totals with containers separated

```python
agg = collections.defaultdict(lambda: [0, 0.0])
for x in r["records"]:
    if "startTime" in x and "endTime" in x:
        k = (x["type"].replace("timeline-record-type-", ""), x.get("eventType"))
        agg[k][0] += 1
        agg[k][1] += x["endTime"] - x["startTime"]
```

Report each bucket as a percentage of `endTime - startTime`, and label `rendering-frame` and `composite` as spans in the output rather than dropping them silently.

---

## Trace Event Format (Chrome DevTools, `chrome://tracing`)

Times are **microseconds**. Events are either a bare top-level array or `{traceEvents: […]}`.

| Field | Meaning |
|---|---|
| `ph` | Phase. `X` complete (carries `dur`), `B`/`E` a begin/end pair on one thread, `I`/`i` instant, `M` metadata, `b`/`e`/`n` async, `P` sample |
| `ts`, `dur` | Start and duration, microseconds |
| `pid`, `tid` | Process and thread; resolve names from `ph:"M"` events named `process_name` / `thread_name` |
| `name`, `cat` | Event name and comma-separated categories |
| `args` | Payload; for `Profile` / `ProfileChunk` this carries the V8 sampler |

Find the renderer main thread from the metadata events (`thread_name` == `CrRendererMain`) and filter to that `tid` before aggregating; a whole-trace total mixes in every worker and browser-process thread.

`RunTask` and the frame-lifecycle events are containers. `X` events nest, so subtract direct children before summing, or aggregate at a single depth.

The CPU sampler in a DevTools trace is a V8 profile riding inside `Profile` and `ProfileChunk` events — reassemble `args.data.cpuProfile` chunks in `ts` order and then treat it as a `.cpuprofile` (below).

---

## V8 `.cpuprofile`

Times are **microseconds**. `timeDeltas[i]` is the gap preceding `samples[i]`; attributing it to `samples[i]` is the conventional reading.

| Field | Holds |
|---|---|
| `nodes[]` | `{id, callFrame:{functionName, url, lineNumber, columnNumber}, hitCount, children:[id]}` — a flat table, tree edges live in `children` |
| `samples[]` | Node id per sample |
| `timeDeltas[]` | Microseconds, index-matched to `samples` |
| `startTime`, `endTime` | Microseconds; their difference is the denominator |

Self time is the weight summed per sampled node id. Total time requires rolling weights up the `children` edges to each ancestor — build a child→parent map first, since the file only stores the forward direction.

Exclude the pseudo-frames `(idle)`, `(program)`, `(root)`, and `(garbage collector)` from the "hot function" list. `(idle)` dominates any profile that includes it, and `(garbage collector)` is a real signal but belongs in its own line rather than competing with application functions.

---

## Gecko / Firefox Profiler

Times are **milliseconds**; sampling interval is `meta.interval`. Often gzipped: decompress with Python's `gzip` module in the script; native `jq` cannot read it.

The format is column-oriented: every "table" is an object of parallel arrays plus a `length`. Resolving one frame means three hops.

- `threads[]` — pick by `isMainThread` / `name`; each thread is a separate profile
- `samples.stack[i]` → index into `stackTable`
- `stackTable.frame[i]` → index into `frameTable`; `stackTable.prefix[i]` → the parent stack row, or null at the root
- `frameTable.func[i]` → index into `funcTable`
- `funcTable.name[i]` → index into the string table (`stringArray`, or `stringTable` in older exports)

Walking `prefix` from a sample's stack row yields frames **leaf first**, terminating at the root. Weight each sample by `samples.weight[i]` when present, otherwise by `meta.interval`.

---

## speedscope

The only format that states its own unit. `profiles[].unit` is one of `none`, `nanoseconds`, `microseconds`, `milliseconds`, `seconds`, `bytes`.

- `shared.frames[]` — `{name, file, line, col}`, referenced by index
- `profiles[].type` — `sampled` (`samples[]` are frame-index arrays, `weights[]` parallel) or `evented` (`events[]` of `{type:"O"|"C", frame, at}`)
- `profiles[].startValue` / `endValue` — the denominator, in `unit`

Sampled stacks are stored **root first**, the opposite of Safari's. Verify with the root-name check before aggregating; getting it backwards inverts self and total time and produces a plausible-looking answer naming the wrong function.
