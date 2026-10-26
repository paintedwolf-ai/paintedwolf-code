---
name: reach-a-network-service
description: Pick the confined network route before HTTP calls, database or broker connections, UDP, local servers, or loopback clients, and recover from bind, connect, DNS, or proxy failures.
---

# Reach a network service

Egress is mediated, so the question is never "is the network up". It is: **which route carries this protocol, and did this call declare it.**

## Routes

| You need | Route | What you do |
|---|---|---|
| One HTTP(S) exchange you compose: API call, webhook, health check, download, upload | `curl` in `command` | `curl -sS -i --fail <url>`; `-H`, `--json`, `-d`, `-u`, `-o`, `-c`/`-b` jars, `--unix-socket` are supported |
| Readable web page or documentation | `fetch_url` | Pass the URL; it returns markdown and pages long documents |
| HTTP(S) from a program: package manager, build, test suite | HTTP proxy variables | Nothing; proxy-aware clients already work |
| Other TCP: Postgres, Redis, Kafka, raw TCP | SOCKS5 (`ALL_PROXY`) | Set `socks_proxy: true` on that `command`, `verify`, or `terminal_open` |
| Git over SSH | Host SOCKS connector | Nothing; already wired |
| UDP (NTP, QUIC, your own DNS query) or a client that ignores proxy variables | Direct IP | `capability_request.direct_ip` with `declared_destinations` |
| A local daemon by socket path | Exact socket | `capability_request.socket_paths` |
| Run a local server (dev server, HMR, test harness) | Local listener | `capability_request.local_listen` with `ports`, or preview static pages with `render_view` / `capture_page` |
| Connect to a service on this machine | Loopback | `capability_request.loopback_connect` with exact `ports` |
| Wait for a service to come up | Readiness probe | `wait` with `http_ready` or `port_ready`, not a request loop |

Mediation is TCP only; nothing creates a mediated UDP route. HTTP proxy variables are injected by default; SOCKS is opt-in because many HTTP clients mishandle `ALL_PROXY`. `socks_proxy` does not widen the sandbox and cannot combine with `direct_ip`.

## Workflow

1. **Name the protocol before requesting anything.** Requesting direct IP for Postgres is a needless escalation when `socks_proxy: true` carries it.
2. **Use direct IP only for UDP or a client that ignores proxy variables** even after `socks_proxy: true`.
3. **Declare destinations with direct IP.** `declared_destinations` takes `host:port` with an optional `tcp://` or `udp://` scheme, e.g. `udp://time.nist.gov:123`. Parsing is all-or-nothing: an entry without a port, or `https://`, drops narrowing for the whole request. Names resolve normally under direct IP.
4. **Put capability on the call that needs it.** Building does not authorize the run. A held `terminal_open` takes the same fields except `direct_ip`; run a direct-network action under `command`, adding `terminal_capture: {}` when a screen picture is needed.
5. **Bind and connect are separate.** Servers need `local_listen`, clients need `loopback_connect`, a harness doing both requests both. A server started with `&` and a `curl` to its port in the same program pair automatically.

## Failures

Application errors are clues, not host decisions. Read the result's structured network observations and boundary refusal first; a timeout alone proves neither a permission gap nor a peer failure. Refusal-to-route table: [reading-a-refusal.md](references/reading-a-refusal.md).

## Do not

- Declare the environment broken from one client error, or let a later call that omitted a field retract an earlier working result.
- Retry an unchanged call after a boundary refusal; it fails the same way.
- Partially clear `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY` (or their lowercase forms), or change the project only to ignore mediation. Use `socks_proxy`, a per-call variable, or a client flag.
- Use `external -- curl` or another HTTP binary for an exchange `curl` in `command` can make.
- Request direct IP to run a server or reach loopback; `local_listen` and `loopback_connect` are smaller.
- Describe a declared destination as restricting the peer. It bounds protocol and port only.
- Narrate host policy. Report what the command proved and the fallback you took.
