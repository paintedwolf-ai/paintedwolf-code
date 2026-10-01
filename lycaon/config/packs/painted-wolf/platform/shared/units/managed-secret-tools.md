---
description: >-
  Creating, listing, or using a credential, API key, token, or password
  through managed secret references, including a secret a human supplies.
slot: conduct
order: 20
attaches: [secret_generate, secret_list, secret_revoke]
hosts: [coordinator, worker]
---
### Managed secrets

Most authenticated work needs none: prefer existing sign-in.{% if agent_has_skill_use_secrets_without_reading_them %} Before handling a credential, read `use-secrets-without-reading-them`.{% endif %} Never inline a real, invented, or weak credential, or fabricate a placeholder. For new random material use `secret_generate`; reuse an existing reference when supplied. Secret tools expose only `{% verbatim %}{{paintedwolf-secret:…}}{% endverbatim %}` references. Pass the returned reference unchanged in the consumer's string argument (such as env/stdin or an HTTP header/body); the host resolves it privately at execution. Project files do not resolve references. Default to chat scope; use project scope only for a named future need. Raw values never enter model requests.{% if profile_has_ask_user %} To receive a human value, call `ask_user` with `response_type: secret` and value-free metadata; its answer is only the reference. Never request credentials as text or choices.{% endif %} A detected value may be tracked and replaced at chat scope without editing its source. Destination approval still applies. Verify through the consumer without printing the value; revoke disposable test references afterward.
For a service you are setting up, include `secret_use.services` with its intended HTTP origins on the setup command or terminal call, alongside the managed reference and normal capability requests. The review can cover the process handoff and later `http_request` use together. Declare only the services this work needs; do not infer that a process or daemon can send the value only there. Reuse the same reference and service origin for subsequent calls.
