---
description: >-
  Making structured HTTP requests: bodies, auth, cookie and token jars,
  loopback capability, virtual hosts, redirects, and timeouts.
slot: procedures
order: 20
attaches: [http_request]
hosts: [coordinator, worker]
---
#### HTTP requests

Use structured method, URL, query, headers, and one body source (`body_json` for JSON, `body_form` for URL-encoded fields, `form` for multipart). Protected references use `auth` or credential fields; `cookie_jar` preserves cookies and `token_jar` preserves tokens (`capture_tokens` captures tokens without exposure; echo with `{{ "{{token:name}}" }}` at the service that issued the token; any other destination is a disclosure the host reviews first). Use `response_body: discard` or `response_path` for output.

When testing runnable local services, exercise changed endpoints with `http_request`: inspect status, headers, and bodies, fixing defects found. Hermetic test suites remain standard when no live server is run.

Loopback needs `capability_request.loopback_connect` with exact ports; local daemons use `unix_socket`. Use `host_header` and `resolve` for virtual hosts. Redirects default to `none`; `safe` reauthorizes hops and drops headers across origins. `timeout_ms` bounds the exchange.
