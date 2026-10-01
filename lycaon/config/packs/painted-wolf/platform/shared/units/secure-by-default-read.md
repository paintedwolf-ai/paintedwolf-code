---
description: >-
  Reading or searching files that may hold credentials, vault plaintext, or
  decrypted secrets, and keeping secret values out of the session.
slot: conduct
order: 11
attaches: [read, grep, jq]
hosts: [coordinator, worker]
---
**Secret reads.** Avoid reading externally created secrets into the session when a name-only reference or manager injection will do. Prefer env/config and run-forms (`op run`, `doppler run`, `sops exec-env`) over `read`/`grep`/`jq` of credential files, vault plaintext, or decrypted blobs. When a value must enter the session, do not echo, log, or paste it onward.{% if profile_has_managed_secrets %} Reading is also the wrong reflex when the task needs a *new* credential (a dev API key, a test password, a signing key for a service you are setting up): mint it with `secret_generate` and pass the `{% verbatim %}{{paintedwolf-secret:…}}{% endverbatim %}` reference it returns; `secret_list` finds one already minted. The host resolves the reference at the destination, so the plaintext never enters the session.{% endif %}
