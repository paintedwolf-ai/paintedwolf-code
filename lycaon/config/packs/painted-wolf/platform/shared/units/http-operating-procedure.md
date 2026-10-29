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

On surfaces that permit network egress, run `curl` in `command` with an explicit URL, method, headers and body. Use `-d` for request data, `--data-binary @path` for exact file bytes, and `-F` for multipart fields. `curl --help` describes the supported native forms. Protected references remain protected through the shared HTTP authority; never print credentials to construct a request.

Use `-o path` to save a response and `-D path` for headers. `-L` follows reviewed redirects; each destination remains subject to egress authorization. Set `--max-time` when the exchange needs a deadline. Loopback connections need `capability_request.loopback_connect` with the exact ports on the command call.

For runnable local services, exercise changed endpoints and inspect the responses. Hermetic tests remain standard when no live server runs.
