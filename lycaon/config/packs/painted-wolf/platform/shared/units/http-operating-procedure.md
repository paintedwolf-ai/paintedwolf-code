---
description: >-
  Making HTTP requests with curl: bodies, credentials, output, loopback
  capability, redirects, and timeouts.
slot: procedures
order: 20
attaches: [command]
hosts: [coordinator, worker]
---
#### HTTP requests

For permitted network egress, run native `curl` in `command` with explicit URL, method, headers and body: `-d` for data, `--data-binary @path` for exact file bytes, `-F` for multipart. `curl --help` lists native forms. Keep protected credential references opaque.

`-o path` saves the body; `-D path` saves headers. `-L` follows reviewed redirects, authorizing each destination. `--max-time` sets a deadline. Loopback requires `capability_request.loopback_connect` with exact ports on the call.{% if profile_has_wait %} Use `wait` with `http_ready`/`port_ready` for readiness.{% endif %}

Exercise changed local endpoints and inspect responses; hermetic tests remain standard when no live service runs.
