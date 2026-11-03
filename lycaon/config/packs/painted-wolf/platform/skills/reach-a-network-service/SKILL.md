---
name: reach-a-network-service
description: Pick the confined network route for HTTP calls, database or broker connections, UDP, local servers, or loopback clients, and recover from bind, connect, DNS, or proxy failures.
---

# Reach a network service

Egress is mediated, so the question is never "is the network up". It is: **which mode carries this protocol, and did this call ask for it.**

## What each mode carries

| You need | Carried by | What you do |
|----------|-----------|-------------|
| One HTTP(S) exchange — API, webhook, login, download or upload | Native `curl` in `command` | Declare URL, method, headers and body with curl flags; `-o` saves output. Use `--cacert`/`--capath` for scoped trust, `--cert`/`--key`/`--pass` for mTLS, `-T file` for upload, and `-v` for diagnostics. Loopback needs `capability_request.loopback_connect`; `--resolve host:port:127.0.0.1` or `--connect-to` retains the production name. Never `external -- curl`. |
| **Reading a web page or documentation** | `fetch_url` | Pass the URL. The default `mode=text` returns readable markdown and pages long documents with `offset` and `limit`; `curl` returns the response bytes |
| HTTP(S) from a process that must speak it itself — a package manager, a build, a test suite | Mediated HTTP proxy (`HTTP_PROXY` / `HTTPS_PROXY`) | Nothing — proxy-aware HTTP clients already work |
| Any other TCP — Postgres, Redis, Kafka, raw TCP | Mediated SOCKS5 `CONNECT` (`ALL_PROXY`) | Pass `socks_proxy: true` on that `command` / `verify` / `terminal_open` |
| Git over SSH | Host SOCKS connector (`LYCAON_SOCKS_PROXY` via Git) | Nothing — already wired under mediation |
| UDP — NTP, QUIC, a DNS query you send yourself | Direct IP only | Request `capability_request.direct_ip` |
| A local daemon by socket path | Exact socket route | Request `capability_request.socket_paths` |
| **Run a local server** — dev server, HMR, test harness that binds a port | Local listener (egress stays mediated) | `capability_request.local_listen` with `ports` on the `command` or `terminal_open` that runs it; `render_view` / `capture_page` preview pages without a server |
| Connect to a service on this machine | Loopback connect | Request `capability_request.loopback_connect`, with exact `ports` when known |
| One command binds and calls a local service | Both local fields | Request both, narrowed to its ports |

Mediation is TCP-only. There is no mediated UDP, and no host rule or allow-list entry creates one.

HTTP proxy variables are injected by default; SOCKS is opt-in with `socks_proxy`, which does not widen the sandbox ([why](references/direct-ip-and-socks.md)).

## Workflow

1. **Name the protocol before requesting anything**, then take its row above. Requesting direct IP for Postgres is a needless escalation when `socks_proxy: true` carries it.
2. **Use direct IP only for UDP or a client that ignores proxy variables** even after `socks_proxy: true`, and declare destinations as `host:port` with an optional `tcp://` or `udp://` scheme; one malformed entry drops narrowing for the whole request. Declaration rules: [direct IP and SOCKS](references/direct-ip-and-socks.md).
3. **Put capability on the call that needs it.** Building does not authorize the run. A held `terminal_open` takes the same fields as `command` except `direct_ip`, so run a direct-network action under `command`, adding `terminal_capture: {}` when it also needs a screen. Never run it once for proof and again under weaker authority for the picture.
4. **Bind and connect are separate.** Servers need `local_listen`, local clients need `loopback_connect`, and a harness doing both requests both. Neither widens public egress or implies the other.

## When something fails

Application errors are clues, not host decisions. Read the result's structured network observations and `boundary_refusal` first; a timeout alone proves neither a permission gap nor a peer failure. Do not retry an unchanged call after a boundary refusal, request direct IP for TCP that mediation carries, or partially strip proxy variables.

Observation-to-next-move table and the full list of pitfalls: [refusals and pitfalls](references/refusals-and-pitfalls.md).
