# Reading a network refusal

Read the result's structured network observations and boundary refusal before the client's own error text. With no observation, leave the cause unresolved.

| Observed need or boundary | Next move |
|---|---|
| Client resolves names locally under mediation | Check its proxy support and configuration; the broker resolves proxied hostnames. Use direct IP only when the required client cannot use the mediated route. |
| UDP or a confirmed raw-network requirement | `command` with `capability_request.direct_ip` and declared destinations. SOCKS carries TCP only. |
| TCP listener | Declare `capability_request.local_listen` with its ports. An `EPERM` string alone does not prove this boundary. |
| Unix socket path | Request that exact `capability_request.socket_paths` entry or the matching host resource. |
| Loopback connection | Declare `capability_request.loopback_connect` for its ports; listening is separate. |
| TCP client that ignores proxy variables | Try its SOCKS route with `socks_proxy: true`; direct IP is the fallback when it cannot use mediation. |
| Client reports missing SOCKS support | Inspect the injected proxy variables and the client's support. HTTP clients should use the HTTP proxy; adding SOCKS variables cannot add client support. |
| `curl` exits 6 (DNS), 7 (connect) or 28 (timeout) | Check the URL, the route above, and readiness with `wait` before retrying. HTTP error statuses exit 0 unless `--fail` is set. |
| Host reports mediation unavailable | Report the broker failure and the bounded alternative; do not infer this from client text. |

`network_mode` describes the route, not a successful exchange. Exit status 0 can accompany handled lookup or connection failures inside the program. Report what structured observations and application results establish, including unresolved gaps.

Mediation is host policy. When a result depended on it (a proxy, a declared destination, a listener grant), say so in the closeout; green here is not proof of the same result in CI or production.
