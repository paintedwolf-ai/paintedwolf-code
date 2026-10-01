# Decision-card reference

Maintained sources of truth: `docs/coordinator-ask-user.md`, the `ask_user` tool schema, and `docs/session.md` for workflow checkpoints. The host stores pending state, enforces caps, synthesizes artifact options, and states structured rejection codes.

## Mode matrix

| Need | Arguments | Do not send |
|---|---|---|
| Open clarification | `prompt`, default `response_type: text` | Unrelated questions in one prompt |
| One choice | `prompt`, `response_type: single_choice`, 2–4 `options` | Fewer than two options; an Other option |
| Several choices | `prompt`, `response_type: multi_choice`, 2–4 `options` | More than six options; an Other option |
| Review visual(s) | `prompt`, `artifacts: [refs]` (1–4), optional `response_type: text` | Confusing review with comparison |
| Compare variants | `prompt`, `purpose: compare`, `artifacts: [refA, refB, …]` (2–4) | More than four variants |

Do not include Other on clarify choice cards — the composer accepts off-menu answers (`off_menu: true`). Artifact refs may be producer UUIDs or session-tree evidence handles such as `render#N` / `page#N`. Review synthesizes Approve / Request changes / Reject (or accepts open text when `response_type: text`). Comparison synthesizes variant choices A/B/C/D. Human approval is preference evidence, not proof that console, geometry, or runtime behavior is correct.

## Timing and blockers

Ask before survey/write only when a product preference changes the work. Do not interview for facts repository or external tools can establish. A rare mid-batch clarification requires all three: proceeding down the wrong mutually exclusive fork wastes work, tools cannot resolve it, and it is not an approval or Settings action.

When a worker reports `request_decision`, answer it directly when the coordinator can decide. If human preference is required, open one card and relay the answered option with `answer_decision`. A sandbox/host/environment blocker card gives concrete human steps, not Allow/Deny language and not an invented grant.

## Pending and answered state

Successful `ask_user` returns pending state and parks the host. The user's reply is the only answer path; there is no caller timeout, countdown, default response, or parallel ask. On the next wake, the same tool result is rewritten as answered. Choice results include `choices[]`; use `response` for free text or Other detail.

If the user names one tab, surface, file, or target, that is already a decision. Proceed in scope unless a distinct unresolved preference materially changes the requested result.
