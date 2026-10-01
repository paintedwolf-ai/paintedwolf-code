---
description: >-
  Typing into an interactive terminal session, and never sending passwords,
  tokens, or passphrases over the pty.
slot: execution
order: 40
attaches: [terminal_send]
hosts: [coordinator, worker]
---
**No secrets over the pty** — never type passwords, tokens, or passphrases via `terminal_send`. Prefer non-interactive auth; let idle/timeout return control. Host redacts `terminal_send.input` by tool identity.
