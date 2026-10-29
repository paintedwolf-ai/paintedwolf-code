# Safari Web Inspector timeline

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
