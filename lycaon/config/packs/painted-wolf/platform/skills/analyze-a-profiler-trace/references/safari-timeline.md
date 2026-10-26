# Safari Web Inspector timeline

Times are **seconds** relative to a page-load epoch. The denominator is `recording.endTime - recording.startTime`.

| Path | Holds |
|---|---|
| `recording.records[]` | `{type, startTime, endTime}` or `{type, timestamp}`; `type` is `timeline-record-type-{script,layout,rendering-frame,screenshots,network,cpu,heap-allocations,media}` |
| `.eventType` on script records | `timer-fired`, `event-dispatched`, `microtask-dispatched`, `animation-frame-fired`, `observer-callback`, `script-evaluated`, plus zero-width `*-installed` / `*-requested` / `*-canceled` bookkeeping |
| `.eventType` on layout records | `composite`, `recalculate-styles`, `invalidate-styles`, `layout`, `forced-layout`, `invalidate-layout`, `paint` |
| `recording.sampleStackTraces[]` | `{timestamp, stackFrames:[{sourceID, name, line, column, url}]}`, **leaf first** |
| `recording.sampleDurations[]` | Weights in seconds, index-matched to `sampleStackTraces` |
| `recording.markers[]` | `{time, type, details}`; `console.timeStamp("label")` arrives as `type: "timestamp"` |
| `recording.memoryPressureEvents[]` | System memory-pressure notifications |
| screenshot records | `imageData` as a `data:image/png;base64,…` URI; never print one |
| network records | HAR-shaped `entry` with `request`, `response`, `timings` |

**Containers:** `rendering-frame` (one per frame, tiles the timeline) and layout `composite` (commit to present; swallows idle and the script inside it). Main-thread cost is script plus layout with both excluded.

A high `forced-layout` to `layout` ratio means synchronous layout thrash. For `recalculate-styles`, read p99 and worst, not the mean.

In a Tauri app the WKWebView serves from `http://127.0.0.1:<port>`, so these recordings are the shipping shell. Native calls appear as `ipc://` network entries.

## Self and total time from sample stacks

Save as `@scratch/trace_agg.py` and run `python3 @scratch/trace_agg.py <trace>`:

```python
import json, collections, sys
r = json.load(open(sys.argv[1]))["recording"]
stacks, weights = r["sampleStackTraces"], r["sampleDurations"]
total = sum(weights)

def key(f):
    return f"{f.get('name') or '(anon)'} @{f.get('url', '').rsplit('/', 1)[-1]}:{f.get('line')}"

self_t, total_t = collections.Counter(), collections.Counter()
for s, w in zip(stacks, weights):
    frames = s["stackFrames"]
    if not frames:
        continue
    self_t[key(frames[0])] += w              # leaf first: index 0 is the leaf
    for k in {key(f) for f in frames}:       # a set counts recursion once
        total_t[k] += w

print(f"{len(weights)} samples, {total:.2f}s sampled")
for k, v in self_t.most_common(15):
    print(f"  self {v * 1000:7.1f}ms {100 * v / total:5.1f}%  {k}")
```

Confirm orientation before trusting it: the last frames should be entry points (`eventHandler`, `promiseReactionJob`, a flush or dispatch function), not application leaves.

## Record totals with containers labeled

```python
agg = collections.defaultdict(lambda: [0, 0.0])
for x in r["records"]:
    if "startTime" in x and "endTime" in x:
        k = (x["type"].replace("timeline-record-type-", ""), x.get("eventType"))
        agg[k][0] += 1
        agg[k][1] += x["endTime"] - x["startTime"]
```

Report each bucket as a share of the recording span. Label `rendering-frame` and `composite` as spans in the output rather than dropping them silently.
