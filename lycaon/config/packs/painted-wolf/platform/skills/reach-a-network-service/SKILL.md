---
name: reach-a-network-service
description: Before network access, local service setup, or bind/listen/connect/DNS/proxy recovery, select the confined route.
---

# Reach a network service

Use this workflow when a command needs something off this machine. Egress is mediated, so the question is never "is the network up." It is: **which mode carries this protocol, and did I ask for the env that steers the client.**

## What each mode carries

| You need | Carried by | What you do |
|----------|-----------|-------------|
| **One HTTP(S) exchange you make yourself** — an API call, webhook, health check, login session, download, upload | `http_request` | Declare it as the call: method, URL, headers, body (`body_json`/`body_form`), `auth`, `cookie_jar`, `response_path`, or `unix_socket`. Loopback needs `capability_request.loopback_connect`. A run-command line of HTTP requests comes back as equivalent `http_request` calls |
| **Reading a web page or documentation** | `fetch_url` | Pass the URL. The default `mode=text` returns readable markdown and pages long documents with `offset` and `limit`; `http_request` would return the raw HTML |
| HTTP(S) from a process that must speak it itself — a package manager, a build, a test suite | Mediated HTTP proxy (`HTTP_PROXY` / `HTTPS_PROXY`) | Nothing — proxy-aware HTTP clients already work |
| Any other TCP — Postgres, Redis, Kafka, raw TCP | Mediated SOCKS5 `CONNECT` (`ALL_PROXY`) | Pass `socks_proxy: true` on that `command` / `verify` / `terminal_open` |
| Git over SSH | Host SOCKS connector (`LYCAON_SOCKS_PROXY` via Git) | Nothing — already wired under mediation |
| UDP — NTP, QUIC, a DNS query you send yourself | Direct IP only | Request `capability_request.direct_ip` |
| A local daemon by socket path | Exact socket route | Request `capability_request.socket_paths` |
| **Run a local server** — dev server, HMR, test harness that binds a port | Local listener (egress stays mediated) | Request `capability_request.local_listen` (declare `ports` to narrow) on the `command` or `terminal_open` that runs the server — or skip the server: `render_view` / `capture_page` preview project pages without one |
| Connect to a service on this machine | Loopback connect | Request `capability_request.loopback_connect`, with exact `ports` when known |
| One command binds and calls a local service | Both local fields | Request both, narrowed to its ports |

Mediation is TCP-only. There is no mediated UDP, and no host rule or allow-list entry creates one.

Default confinement injects the **HTTP** front door only. Co-injecting SOCKS into every process steers HTTP clients that honor `ALL_PROXY` onto an optional SOCKS stack they often lack — that failure is a client/proxy-scheme mismatch, not a down broker. Opt in with `socks_proxy` when the client actually needs SOCKS. That flag does not widen the sandbox.

## Workflow

1. **Name the protocol before requesting anything.** An HTTP(S) exchange you would compose yourself is `http_request`, not a runner client; reading a web page or documentation is `fetch_url`. HTTP(S) from a process is already carried. Other TCP needs `socks_proxy: true` so the client sees `ALL_PROXY`. Requesting direct IP for Postgres is a needless escalation.
2. **The other reason for direct IP is a client that ignores proxy variables** — not the protocol. If a tool refuses `HTTPS_PROXY` / `ALL_PROXY` even after `socks_proxy: true`, direct IP is the fallback even for TCP.
3. **When you request direct IP, declare your destinations.** `declared_destinations` takes `host:port` with an optional `tcp://` or `udp://` scheme. Declaring narrows the sandbox to those transports — `udp://time.nist.gov:123` permits UDP/123 and refuses everything else. Declaring nothing leaves every protocol and port open, which is a larger request for the same work. Do not combine `socks_proxy` with `direct_ip`.
4. **Get the format right or you lose the narrowing.** Parsing is all-or-nothing: an entry without a port, or a scheme like `https://` that only implies one, drops narrowing for the whole request.
5. **Resolve names normally under direct IP.** It is the one mode where the system resolver works, so you do not need to hand-resolve literal addresses.
6. **Put capability on the call that needs it.** Building does not authorize the run. A held `terminal_open` takes the same capability fields as `command` except `direct_ip` — a one-action lease cannot cover a session held across turns. For a direct-network action, run the exact program under `command`; when the result also needs a terminal picture, add `terminal_capture: {}` to that same call. Never run it once for proof and again under weaker authority for the picture. Likewise, set `socks_proxy` on the process needing SOCKS.
7. **Bind and connect are separate.** Servers need `local_listen`, local clients need `loopback_connect`, and a harness doing both requests both. Neither widens public egress or implies the other.

## Reading a refusal

Application errors are clues, not host decisions. Read `command_output`’s structured network observations and `boundary_refusal` first. With no observation, keep the cause unresolved; a timeout alone proves neither permission nor peer failure.

| Observed need or boundary | Next move |
|---------------------------|-----------|
| Client resolves locally under mediation | Check proxy support and configuration; the broker resolves proxied hostnames. Use direct IP only when the required client cannot use the mediated route. |
| UDP or a confirmed raw-network requirement | `command` with `capability_request.direct_ip` and declared destinations. SOCKS carries TCP only. |
| TCP listener | Declare `capability_request.local_listen` with its ports. An `EPERM` string alone does not prove this boundary. |
| Unix socket path | Request that exact `capability_request.socket_paths` route or the matching host resource. |
| Loopback connection | Declare `capability_request.loopback_connect` for its ports; listening is separate. |
| Proxy-ignoring TCP client | Try its supported SOCKS route with `socks_proxy: true`; direct IP is the fallback when it cannot use mediation. |
| Client reports missing SOCKS support | Inspect injected proxy fields and client support. HTTP clients should use the HTTP proxy; adding SOCKS env cannot install client support. |
| Host reports mediation unavailable | Report the broker failure and the bounded alternative; do not infer this from client text. |

`network_mode` describes the route, not a successful exchange. Even `exit_code: 0` can accompany handled lookup or connection failures. Report what structured observations and application results establish, including unresolved gaps.

## Do not

- **Do not declare the environment broken from one client error.** Recover through the matching route when supported by observations; report a proven broker failure or unresolved limitation honestly.
- **Do not narrate host policy.** Report what the command proved and the fallback you took. Do not explain why a command paused unless they ask.
- **Do not let a later omitted field retract an earlier working result.** A later `command` that omitted `direct_ip` and failed at lookup is that invocation's boundary, not evidence the earlier run was wrong or that the host cannot reach the network.
- **Do not reshape the project for this host box.** Mediation is host policy. Prefer `socks_proxy`, one-shot env, or client flags on the invocation over durable product changes whose only purpose is to ignore injected proxy vars. Close out when a result was mediation-conditioned; green here is not ambient CI parity.
- **Do not partially strip proxy environment variables** to "fix" mediation. Clearing only some of `HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY` (and their lowercase forms) leaves the others and reproduces the same failure. Prefer `socks_proxy` for SOCKS clients, or disable env trust in the HTTP client when the process must ignore mediation.
- **Do not retry the same call unchanged.** A boundary refusal is deterministic; the second attempt fails identically.
- **Do not request direct IP for TCP that mediation already carries** (HTTP via default env, other TCP via `socks_proxy: true`).
- **Do not run a shell HTTP client for an exchange `http_request` represents.** Readiness is `wait` with `http_ready` / `port_ready`, not a request loop.
- **Do not request direct IP just to run a server.** `local_listen` covers the bind while egress stays mediated — a strictly smaller request than unobserved direct networking.
- **Do not use direct IP for loopback.** `loopback_connect` leaves external egress unchanged.
- **Do not describe a declared destination as restricting *where* you connected.** It bounds protocol and port. The host cannot restrict the peer and does not claim to.
