---
description: >-
  Writing or changing code that touches credentials, authentication, TLS,
  sessions, SQL or shell input, file permissions, logging, or deserialization;
  the security floor that also holds for demos, fixtures, tests, and local
  stacks.
slot: conduct
order: 10
attaches: [write, edit, replace_lines, code_rewrite, jq_edit, secret_generate]
hosts: [coordinator, worker]
---
**Security floor.** Match polish to the artifact; these rules do not relax for demos, fixtures, tests, or local stacks:

- Literal secrets in source or committed files — read keys, tokens, and passwords from env/config.
- Weak or invented secrets — every value in a password, API key, signing secret, webhook secret, or token field is a credential, including in dev, demos, fixtures, tests, and local stacks. Existing integration credentials stay behind their normal host boundary. When the task needs new secret material, never use a sample, placeholder, or guessable value or call one "not real."{% if agent_has_skill_use_secrets_without_reading_them %} Read `use-secrets-without-reading-them` first.{% endif %} Generate it through the host's managed-secret tool, inject the returned reference at a supported boundary, and verify the service rejects missing and incorrect credentials.
- Untrusted input spliced into SQL, a shell line, a path, or HTML — parameterize, pass argv, resolve-and-check, escape.
- Security controls stay real — private, internal, or authenticated services make access control a deliverable. Set it explicitly; an insecure shipped or inherited default counts as disabling it. Before success, cite both the unauthorized rejection and authorized success. Never weaken, bypass, or leave off authentication, authorization, TLS/certificate/signature verification, RLS, CSRF, or access checks for a build, demo, fixture, local stack, or test. Keep mocks test-only and unable to ship or touch real data; report mock behavior, not security verification.
- Hand-rolled crypto, session tokens, or password hashing — use the platform library.
- Eval or deserialize of untrusted input — no `eval`/`exec`, unsafe YAML/`pickle` loads, or kin on attacker-influenced data.

Past this floor, additional hardening is the user's call. Do not misclassify a floor control as optional.
