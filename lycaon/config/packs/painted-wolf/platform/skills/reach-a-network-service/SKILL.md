---
name: reach-a-network-service
description: Pick the confined network route for HTTP calls, database or broker connections, UDP, local servers, or loopback clients, and recover from bind, connect, DNS, or proxy failures.
---

# Reach a network service

Choose the route for the protocol and declare it on the call that uses it.

## What each mode carries

| You need | Carried by | What you do |
|----------|-----------|-------------|
| One HTTP(S) exchange — API, webhook, login, download or upload | Native `curl` in `command` | Declare URL, method, headers and body with curl flags; `-o` saves output. Use `--cacert`/`--capath` for scoped trust, `--cert`/`--key`/`--pass` for mTLS, `-T file` for upload, and `-v` for diagnostics. Loopback needs `capability_request.loopback_connect`; `--resolve host:port:127.0.0.1` or `--connect-to` retains the production name. Never `external -- curl`. |
{% if profile_has_fetch_url %}| **Reading a web page or documentation** | `fetch_url` | Pass the URL. Reads extracted markdown, cached `offset`/`limit` ranges, or screened images without writing files; use `curl` for response bytes |
{% endif %}
| HTTP(S) from a process that must speak it itself — a package manager, a build, a test suite | Mediated HTTP proxy (`HTTP_PROXY` / `HTTPS_PROXY`) | Nothing — proxy-aware HTTP clients already work |
| Any other TCP — Postgres, Redis, Kafka, raw TCP | Mediated SOCKS5 `CONNECT` (`ALL_PROXY`) | Pass `socks_proxy: true` on that `command` / `verify` / `terminal_open` |
| Git over SSH | Host SOCKS connector (`LYCAON_SOCKS_PROXY` via Git) | Nothing — already wired under mediation |
| UDP — NTP, QUIC, a DNS query you send yourself | Direct IP only | Request `capability_request.direct_ip` |
| A local daemon by socket path | Exact socket route | Request `capability_request.socket_paths` |
| **Run a local server** — dev server, HMR, test harness that binds a port | Local listener (egress stays mediated) | `capability_request.local_listen` with `ports` on the `command` or `terminal_open` that runs it; `render_view` / `capture_page` preview static pages without a server |
| Connect to a service on this machine | Loopback connect | Request `capability_request.loopback_connect`, with exact `ports` when known |
| One command binds and calls a local service | Both local fields | Request both, narrowed to its ports |

## Requesting access

HTTP clients receive proxy variables by default. Other TCP clients can opt in to SOCKS with `socks_proxy: true`. For UDP or a client that cannot use either proxy, request direct IP under `command`. Declare destinations as `host:port` with an optional `tcp://` or `udp://` scheme. One malformed entry removes narrowing for the whole request. [Direct IP and SOCKS](references/direct-ip-and-socks.md)

Building and running a program are separate calls. Declare access where the program runs. For a direct-IP run that needs a screen, add `terminal_capture: {}` to that same `command`. A held `terminal_open` supports the other network fields.

## When something fails

Application errors are clues, not host decisions. Read the result's structured network observations and `boundary_refusal` first; a timeout alone proves neither a permission gap nor a peer failure. Do not retry an unchanged call after a boundary refusal, request direct IP for TCP that mediation carries, or partially strip proxy variables.

Observation-to-next-move table and the full list of pitfalls: [refusals and pitfalls](references/refusals-and-pitfalls.md).

## Use a named HTTP session

Pass `--managed-cookie-jar NAME` to read and update a chat or project cookie jar. File jars use `-b` and `-c`.

Pass `--managed-token-jar NAME --capture-token session=json:session.token` to hold a response field, or use `session=header:X-Session` for a response header. Later requests can place `{% verbatim %}{{token:session}}{% endverbatim %}` in an inline header or body. Keep the token in its jar. Managed transfers decode the response before capture and screen delivered headers, body and files.
