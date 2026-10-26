# Trace formats: identification

Query the top-level keys first (`jq 'keys' <trace>` in `command` for an uncompressed file; otherwise print `sorted(d)` from the script), then the container's keys. Match the first row that fits.

| Shape you see | Format | Unit | Stack order | Details |
|---|---|---|---|---|
| `{version, recording:{records, sampleStackTraces, sampleDurations}}` | Safari Web Inspector timeline | seconds | leaf first | [safari-timeline.md](safari-timeline.md) |
| `{traceEvents:[…]}` or a bare array of `{ph, ts, name}` | Trace Event Format (Chrome DevTools, Perfetto JSON, `chrome://tracing`) | µs | tree of events | [json-profile-formats.md](json-profile-formats.md#trace-event-format) |
| `{nodes:[{callFrame, hitCount, children}], samples, timeDeltas}` | V8 `.cpuprofile` (`node --cpu-prof`, DevTools CPU profile) | µs | node tree | [json-profile-formats.md](json-profile-formats.md#v8-cpuprofile) |
| `{meta:{interval}, threads:[{stackTable, frameTable, funcTable}]}` | Gecko / Firefox Profiler, often `.json.gz` | ms | leaf first via `prefix` | [json-profile-formats.md](json-profile-formats.md#gecko--firefox-profiler) |
| `{shared:{frames}, profiles:[{unit, samples, weights}]}` | speedscope | stated in `unit` | root first | [json-profile-formats.md](json-profile-formats.md#speedscope) |
| gzip magic `1f 8b`, not JSON after decompression | pprof protobuf | per profile | — | below |
| protobuf, `.perfetto-trace` | Perfetto | ns | — | below |
| text lines beginning `tick,` / `code-creation,` | V8 `--prof` log | — | — | below |

Extensions do not distinguish these: Safari, Chrome, cpuprofile, Gecko, and speedscope files are all `.json`. Check the first two bytes with `xxd -l 2 <trace>` before assuming JSON.

## Non-JSON formats

Do not hand-parse them. Use the producer's own tooling and declare its runtime in `capability_request.host_resources`:

- pprof: `go tool pprof -top -nodecount=40 <file>` or `-traces` (declare `go`).
- Perfetto: `trace_processor_shell` with SQL over the slice and sample tables.
- V8 log: `node --prof-process <isolate-log>` (declare `node`).

## Checks that apply to every format

- The denominator is the recording's own span: end minus start in the format's unit.
- Exclude pseudo-frames `(idle)`, `(program)`, and `(root)` from hot lists; report `(garbage collector)` separately.
- Confirm stack orientation from the data: the root end holds entry points (`(root)`, an event handler, `promiseReactionJob`, a dispatch or flush function).
