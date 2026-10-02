# Reporting

Painted Wolf Code is open source and free. There is no support contract: no guaranteed
reply, no guaranteed fix, no guaranteed timeline. Reports are still welcome.

These routes exist so a report lands in the right place. Landing there is not a promise
that it will be acted on.

## Which door

| You have | Go here |
|----------|---------|
| A question, or "how do I…" | **Discussions.** Answers come from whoever happens to know, when they have time. |
| An idea or feature request | **Discussions.** An issue is opened once there is something specific to build. |
| A bug you can reproduce in the app itself | **Issues** → *Bug report* |
| The app will not start or install | **Issues** → *App won't start* |
| The agent stayed inside its permissions and did something you did not want | **Issues** → *The agent did something wrong.* Read that form first — not every such report is a defect. |
| The agent escaped its boundary — wrote outside an approved root, reached a host it should not, or exposed a secret | **Private advisory.** Repository **Security** tab → **Report a vulnerability**. Never a public issue. See [`SECURITY.md`](SECURITY.md). |
| A broken page, link, or wrong documentation on paintedwolf.ai | **Issues** → *Website problem* |
| A security vulnerability in paintedwolf.ai | **Private advisory**, same as above. |
| A site impersonating Painted Wolf | Email **info@paintedwolf.ai**. Do not link it in a public issue. |
| You want to contribute code | [`CONTRIBUTING.md`](CONTRIBUTING.md) — read the pull-request section first |

Issues are a public record of work that might be done. An open issue is not a promise that
it will be. Everything that is not a report starts in Discussions.

## In-bounds agent reports

The agent uses a model. You can attach any model. Model output is not deterministic.

The host enforces the boundary, injects the prompts, offers the tools, raises the gates,
applies the overlays, and implements the loop and stall detection it claims. It does not
determine the next token.

File *The agent did something wrong* when you think the **product** should have stopped the
action:

- an approval you expected never appeared
- project policy was ignored, and you believe it was injected
- it looped or stalled in a way the product claims to detect
- a shipped default model and shipped prompts fail the same task consistently
- the blast radius should have been smaller than Review, undo, or scope allowed

Do not file it for a single bad edit, an ignored instruction, or a bad call from a model you
chose. That is expected. If you are unsure, the form has a place to say so. A report that is
a one-off judgment from a model you chose will be closed as expected behaviour.

Containment escapes are a different door. [`SECURITY.md`](SECURITY.md).

## What a useful report contains

The issue forms require these because a report without them cannot be acted on, even when
someone has time:

1. **What happened, what you expected, and how to reproduce it** — numbered, from a fresh session.
2. **System information.** In the app: **Settings → Advanced → Diagnostics → System information →
   Copy for bug report**. This is versions and readiness checks only. It carries no keys and nothing
   about your projects, so it is safe to paste publicly.
3. **A report id** — the filename of a saved diagnostics bundle (see below).

An in-bounds agent report also needs the **model and provider**, whether that model was a
shipped default or one you chose, and why you think the host should have stopped it.

## The diagnostics bundle stays on your machine

**Settings → Advanced → Diagnostics → Save diagnostics bundle** writes a zip next to nothing else —
it is not uploaded, and the app has no code path that could upload it. The bundle contains:

- `health.json` and `preflight.json` — versions and environment readiness,
- `config/` — your settings files, with secrets removed,
- `logs/` — the tail of each recent log file, with secrets removed.

Credential stores are excluded outright, not redacted.

**Do not attach it to a public issue.** Secret *values* are scrubbed, but the bundle still
contains your file paths, project names, and recent log lines — which can identify your employer,
your clients, or your source code. A GitHub attachment is public permanently.

Instead, paste the **filename** into the report id field. It looks like:

```text
painted-wolf-code-diagnostics-20260729-141530.zip
```

Keep the file. If a maintainer needs it, they will ask on the issue — send it to
**info@paintedwolf.ai** with the issue number in the subject. Do not email it unasked, and
do not email for help. That mailbox is not a support line.

## What to expect

A report may be labelled, closed, or sit. Filing one does not entitle you to a reply.

Issues missing required information get labelled and, if nothing arrives, closed after a stated
grace period — the bot names exactly what is missing, and a single comment reopens the thread.
Closing is a queue-management action, not a judgement about whether your bug is real, and not
a commitment to fix the ones that stay open.

## Security

Never report a vulnerability in a public issue. [`SECURITY.md`](SECURITY.md) has the scope, the
private advisory flow, and what is explicitly out of scope for the current local-first threat
model.
