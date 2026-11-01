# Chrome, V8, Firefox, and speedscope profiles

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


## Gecko / Firefox Profiler

Times are **milliseconds**; sampling interval is `meta.interval`. Often gzipped: decompress with Python's `gzip` module in the script; the `jq FILTER FILE` command cannot read compressed input.

The format is column-oriented: every "table" is an object of parallel arrays plus a `length`. Resolving one frame means three hops.

- `threads[]` — pick by `isMainThread` / `name`; each thread is a separate profile
- `samples.stack[i]` → index into `stackTable`
- `stackTable.frame[i]` → index into `frameTable`; `stackTable.prefix[i]` → the parent stack row, or null at the root
- `frameTable.func[i]` → index into `funcTable`
- `funcTable.name[i]` → index into the string table (`stringArray`, or `stringTable` in older exports)

Walking `prefix` from a sample's stack row yields frames **leaf first**, terminating at the root. Weight each sample by `samples.weight[i]` when present, otherwise by `meta.interval`.


## speedscope

The only format that states its own unit. `profiles[].unit` is one of `none`, `nanoseconds`, `microseconds`, `milliseconds`, `seconds`, `bytes`.

- `shared.frames[]` — `{name, file, line, col}`, referenced by index
- `profiles[].type` — `sampled` (`samples[]` are frame-index arrays, `weights[]` parallel) or `evented` (`events[]` of `{type:"O"|"C", frame, at}`)
- `profiles[].startValue` / `endValue` — the denominator, in `unit`

Sampled stacks are stored **root first**, the opposite of Safari's. Verify with the root-name check before aggregating; getting it backwards inverts self and total time and produces a plausible-looking answer naming the wrong function.
