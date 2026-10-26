# JSON profile formats

## Trace Event Format

Chrome DevTools Performance, Perfetto JSON export, and `chrome://tracing`. Times are **microseconds**. Events are a bare top-level array or `{traceEvents: […]}`.

| Field | Meaning |
|---|---|
| `ph` | Phase: `X` complete (has `dur`), `B`/`E` begin/end on one thread, `I`/`i` instant, `M` metadata, `b`/`e`/`n` async, `P` sample |
| `ts`, `dur` | Start and duration in µs |
| `pid`, `tid` | Process and thread; names come from `ph:"M"` events named `process_name` / `thread_name` |
| `name`, `cat` | Event name and comma-separated categories |
| `args` | Payload; `Profile` / `ProfileChunk` carry the V8 sampler |

Find the renderer main thread (`thread_name` == `CrRendererMain`) and filter to its `tid` before aggregating; a whole-trace total mixes every worker and browser thread.

`RunTask` and frame-lifecycle events are containers. `X` events nest, so subtract direct children before summing, or aggregate one depth.

The DevTools CPU sampler rides inside `Profile` and `ProfileChunk` events. Reassemble `args.data.cpuProfile` chunks in `ts` order, then treat the result as a `.cpuprofile`.

## V8 cpuprofile

Times are **microseconds**. `timeDeltas[i]` is the gap before `samples[i]`; attribute it to `samples[i]`.

| Field | Holds |
|---|---|
| `nodes[]` | `{id, callFrame:{functionName, url, lineNumber, columnNumber}, hitCount, children:[id]}`; edges live only in `children` |
| `samples[]` | Node id per sample |
| `timeDeltas[]` | µs, index-matched to `samples` |
| `startTime`, `endTime` | µs; their difference is the denominator |

Self time is the weight summed per sampled node id. Total time rolls weights up to every ancestor; build a child-to-parent map first because the file stores only forward edges.

## Gecko / Firefox Profiler

Times are **milliseconds**; the sampling interval is `meta.interval`. Often gzipped: decompress with Python's `gzip` module in the script.

Tables are column-oriented objects of parallel arrays plus `length`. Resolving one frame takes three hops:

- `threads[]`: pick by `isMainThread` or `name`; each thread is a separate profile.
- `samples.stack[i]` indexes `stackTable`.
- `stackTable.frame[i]` indexes `frameTable`; `stackTable.prefix[i]` is the parent stack row, null at the root.
- `frameTable.func[i]` indexes `funcTable`.
- `funcTable.name[i]` indexes the string table (`stringArray`, or `stringTable` in older exports).

Walking `prefix` from a sample's row yields frames **leaf first**. Weight each sample by `samples.weight[i]` when present, otherwise by `meta.interval`.

## speedscope

The only format that states its unit: `profiles[].unit` is `none`, `nanoseconds`, `microseconds`, `milliseconds`, `seconds`, or `bytes`.

- `shared.frames[]`: `{name, file, line, col}`, referenced by index.
- `profiles[].type`: `sampled` (`samples[]` are frame-index arrays with parallel `weights[]`) or `evented` (`events[]` of `{type:"O"|"C", frame, at}`).
- `profiles[].startValue` / `endValue`: the denominator, in `unit`.

Sampled stacks are stored **root first**, the opposite of Safari. Check the root names before aggregating; reversing the order swaps self and total time and names the wrong function.
