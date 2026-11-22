---
name: reach-a-network-service
description: Pick the confined network route for HTTP calls, database or broker connections, UDP, local servers, or loopback clients, and recover from bind, connect, DNS, or proxy failures.
---

# Reach a network service

Choose the route for the protocol and declare it on the call that uses it.

## What each mode carries

| You need | Carried by | What you do |
|----------|-----------|-------------|
{% if profile_command_network_egress %}| One HTTP(S) exchange — API, webhook, login, download or upload | Native `curl` in `command` | Pass URL, method, headers and body via curl flags (`-o` saves output, `--cacert`/`--capath` scoped trust, `--cert`/`--key`/`--pass` mTLS, `-T` upload, `-v` diagnostics). Loopback needs `capability_request.loopback_connect`; `--resolve host:port:127.0.0.1` or `--connect-to` keeps the host name.{% if can_command_external_selector %} Never `external -- curl`.{% endif %} |
{% endif %}{% if profile_has_fetch_url %}| **Reading a web page or documentation** | `fetch_url` | Pass the URL for extracted markdown, cached `offset`/`limit` ranges, or screened images{% if profile_command_network_egress %}; use `curl` for raw bytes{% endif %} |
{% endif %}| HTTP(S) from a process — package manager, build, test suite | Mediated HTTP proxy (`HTTP_PROXY` / `HTTPS_PROXY`) | Nothing — proxy-aware HTTP clients already work |
| Any other TCP — Postgres, Redis, Kafka, raw TCP | Mediated SOCKS5 `CONNECT` (`ALL_PROXY`) | Pass `socks_proxy: true` on `verify` / `terminal_open`{% if profile_command_external_exec %} / `command`{% endif %} |
| Git over SSH | Host SOCKS connector (`LYCAON_SOCKS_PROXY` via Git) | Nothing — already wired under mediation |
| UDP — NTP, QUIC, custom DNS query | Direct IP only | Request `capability_request.direct_ip` |
| Local daemon by socket path | Exact socket route | Request `capability_request.socket_paths` |
| **Run a local server** — dev server, HMR, test harness | Local listener (egress mediated) | `capability_request.local_listen` with `ports` on `terminal_open`{% if profile_command_external_exec and profile_command_mutation %} or `command`{% endif %}; `render_view` / `capture_page` preview static pages without a server |
| Connect to a local service | Loopback connect | Request `capability_request.loopback_connect`, with exact `ports` when known |
| Bind and call a local service | Both local fields | Request both, narrowed to its ports |

## Requesting access

HTTP clients receive proxy variables by default.{% if runner_socks_proxy_tools %} Other TCP clients opt in to SOCKS with `socks_proxy: true`.{% endif %} For UDP or a client that cannot use either proxy, request direct IP{% if profile_command_external_exec and profile_command_mutation %} under `command`{% else %} on `verify`{% endif %}. Declare destinations as `host:port` (optional `tcp://` or `udp://`); one malformed entry drops narrowing for the whole request. [Direct IP and SOCKS](references/direct-ip-and-socks.md)

Building and running a program are separate calls; declare access where the program runs.{% if profile_has_terminal_capture %} For a direct-IP run needing a screen, add `terminal_capture: {}` to that `command`.{% endif %} A held `terminal_open` supports the other network fields.

## When something fails

Application errors are clues, not host decisions. Read structured network observations and `boundary_refusal` first; a timeout alone proves neither a permission gap nor a peer failure. Do not retry an unchanged call after a boundary refusal, request direct IP for TCP that mediation carries, or partially strip proxy variables. See [refusals and pitfalls](references/refusals-and-pitfalls.md).
{% if profile_command_network_egress %}
## Use a named HTTP session

Pass `--managed-cookie-jar NAME` for chat/project cookies (file jars use `-b`/`-c`), and `--managed-token-jar NAME --capture-token session=json:session.token` (or `session=header:X-Session`) to hold a token for later `{% verbatim %}{{token:session}}{% endverbatim %}` headers or bodies without exposing values. Managed transfers decode before capture and screen headers, body and files.
{% endif %}