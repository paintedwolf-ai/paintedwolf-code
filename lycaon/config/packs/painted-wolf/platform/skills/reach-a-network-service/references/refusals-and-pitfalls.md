# Network refusals and pitfalls

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
- **Do not run an installed shell HTTP client through `external` for an exchange `http_request` or the native command form represents.** Readiness is `wait` with `http_ready` / `port_ready`, not a request loop.
- **Do not request direct IP just to run a server.** `local_listen` covers the bind while egress stays mediated — a strictly smaller request than unobserved direct networking.
- **Do not use direct IP for loopback.** `loopback_connect` leaves external egress unchanged.
- **Do not describe a declared destination as restricting *where* you connected.** It bounds protocol and port. The host cannot restrict the peer and does not claim to.
