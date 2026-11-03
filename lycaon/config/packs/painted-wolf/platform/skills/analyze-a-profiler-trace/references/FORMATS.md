# Trace formats

## Identify by shape

Query the top-level keys first (`jq 'keys' <trace>` in `command` for uncompressed files up to 64 MiB; otherwise print `sorted(d)` from the script), then the container's keys. Match the first row that fits.

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

## Field maps and aggregations

- [Safari Web Inspector timeline](references/safari-timeline.md): record and sample paths, containers, and the self/total-time script.
- [Chrome, V8, Firefox, and speedscope](references/json-profile-formats.md): Trace Event phases and threads, `.cpuprofile` nodes and deltas, Gecko column tables, speedscope units and stack order.
