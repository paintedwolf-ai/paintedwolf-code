---
name: ask-for-a-decision
description: Use ask_user for a blocking product choice, required human action, or review of one to four visuals.
# Two parties: a worker raises, the coordinator answers. Neither holds both halves.
optional_tools:
  - ask_user
  - answer_decision
  - wait
  - request_decision
---

# Ask for a decision

1. Isolate one decision that changes the next action. Resolve repository facts with tools and honor any target, surface, or file the user already named instead of asking for confirmation.
2. Choose a legitimate ask:
   - Before the first writer, clarify a product fork involving target, stack/runtime, shape, scope, or done-definition.
   - Mid-batch, ask only when a mutually exclusive preference fork would waste current work and survey cannot resolve it.
   - For a host/environment blocker, give plain-language steps the human can take; never frame the card as an approval or grant.
   - For visual judgment, attach 1–4 artifacts for review/feedback, or 2–4 for comparison between alternative directions.
3. Write a short prompt for a human glance. Prefer `single_choice` or `multi_choice` with two to four crisp options over open text. Do not add an Other option — the composer accepts off-menu answers. Keep one decision under 800 Unicode code points; six options is the host maximum.
4. For visual review or open feedback across 1–4 artifacts, use default `purpose: review` (pass `response_type: text` for free-form critique, or omit for Approve/Changes/Reject). Use `purpose: compare` only when asking the user to choose one alternative among 2–4 variants.
5. Call `ask_user` once. A successful call parks the host until the composer-dock card is answered; do not call `wait`, add a timeout/default, or open another ask while one is pending.
6. On wake, read the answered tool result and prefer `choices[]` over splitting free-form `response`. `off_menu: true` resolves the fork the same as a listed pick. Apply the decision or, for a worker `request_decision`, relay the selected option through `answer_decision`.
7. Branch on structured rejection `Code:` and correct the card. Never use asks for scanner triage, Settings nudges, workflow approvals, confinement exceptions, or automatic permission grants.

See [decision cards](references/decision-cards.md) for the mode matrix, argument shapes, blocker/worker handling, and held boundaries.
