---
name: ask-for-a-decision
description: Use ask_user for a blocking product choice, required human action, or review of one to four visuals.
metadata:
  paintedwolf.any_of_tools: ask_user, request_decision
---

# Ask for a decision

1. Isolate one decision that changes the next action. Resolve repository facts with tools and honor any target, surface, or file the user already named instead of asking for confirmation.
2. Choose a legitimate ask:
   - Before the first writer, clarify a product fork involving target, stack/runtime, shape, scope, or done-definition.
   - Mid-batch, ask only when a mutually exclusive preference fork would waste current work and survey cannot resolve it.
   - For a host/environment blocker, give plain-language steps the human can take; never frame the card as an approval or grant.
   - For visual judgment, attach 1–4 artifacts for review/feedback, or 2–4 for comparison between alternative directions.
3. {% if profile_has_ask_user %}Write a short prompt for a human glance. Prefer `single_choice` or `multi_choice` with two to four crisp options over open text. Do not add an Other option — the composer accepts off-menu answers. Keep one decision under 800 Unicode code points; six options is the host maximum.{% else %}Write a short question that cites the real constraint, with two or more concrete, mutually exclusive options.{% endif %}
4. {% if profile_has_ask_user %}For visual review or open feedback across 1–4 artifacts, use default `purpose: review` (pass `response_type: text` for free-form critique, or omit for Approve/Changes/Reject). Use `purpose: compare` only when asking the user to choose one alternative among 2–4 variants.{% else %}Set `blocker_class` honestly: `decision` when the coordinator may answer, `checkpoint` when the user must approve, `sandbox` only for a confinement or host-environment block after the write root is declared — never for missing paths, typos, or a wrong cwd. Attach `artifact_id` for one artifact or `artifact_ids` for two to four variants when the choice needs visual judgment.{% endif %}
{% if profile_has_ask_user %}5. Call `ask_user` once. A successful call parks the host until the composer-dock card is answered; do not {% if profile_has_wait %}call `wait`, {% endif %}add a timeout/default, or open another ask while one is pending.{% endif %}{% if profile_has_request_decision %}5. Call `request_decision` once. It suspends the run until the coordinator answers; keep the same workspace for the resume, and do not call `complete_leg` while the ask is pending.{% endif %}
6. {% if profile_has_ask_user %}On wake, read the answered tool result and prefer `choices[]` over splitting free-form `response`. `off_menu: true` resolves the fork the same as a listed pick. Apply the decision{%if profile_has_answer_decision %}, or relay the selected option through `answer_decision`{% endif %}.{% endif %}{% if profile_has_request_decision %}On wake, read the coordinator's answer, apply the selected option, and continue the leg on the same workspace.{% endif %}
7. Branch on structured rejection `Code:` and correct the card. Never use asks for scanner triage, Settings nudges, workflow approvals, confinement exceptions, or automatic permission grants.

See [decision cards](references/decision-cards.md) for the mode matrix, argument shapes, blocker/worker handling, and held boundaries.
