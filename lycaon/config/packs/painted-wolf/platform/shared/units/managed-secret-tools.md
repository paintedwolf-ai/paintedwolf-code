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

Prefer existing sign-in.{% if agent_has_skill_use_secrets_without_reading_them %} Read `use-secrets-without-reading-them` before handling credentials.{% endif %} {% if profile_has_secret_generate %}For new random material use `secret_generate`; reuse{% else %}Reuse{% endif %} existing `{% verbatim %}{{paintedwolf-secret:…}}{% endverbatim %}` references unchanged in consumer string arguments (env/stdin, HTTP header/body) — the host resolves them privately at execution, never in project files or model requests. Default to chat scope; revoke disposable test references afterward.{% if profile_has_ask_user %} Request human secrets via `ask_user` (`response_type: secret`, value-free metadata), never as text or choices.{% endif %}
When setting up a service, pass `secret_use.services` with only its needed HTTP origins alongside the reference on the setup command or terminal call, and reuse both on subsequent calls.
