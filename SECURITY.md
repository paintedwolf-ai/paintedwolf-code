# Security Policy

Painted Wolf Code (engine codename **Lycaon**) is a local-first AI coding agent. Its
threat model, trust boundaries, and containment posture are documented in
[`docs/security.md`](docs/security.md) — read that for the design-level detail behind
this policy.

## Reporting a vulnerability

**Please do not open a public issue for a security vulnerability.**

Report privately through GitHub's **Report a vulnerability** flow (repository
**Security** tab → **Report a vulnerability**), which opens a private advisory visible
only to maintainers. Include:

- affected component (Lycaon sidecar, Den frontend, a bundled pack, a dependency, or the
  paintedwolf.ai website),
- version / commit,
- a description and, where possible, a minimal reproduction,
- the impact you believe it has.

We will acknowledge the report. If we confirm it, we will work toward a fix and
coordinate disclosure when one is available. There is no guaranteed timeline.

## Supported versions

Security fixes are provided for the latest stable release. Before a stable release
exists, fixes land on the default branch (`main`).

## Scope

In scope for a report:

- **Local API abuse / IDOR** — session hijack, settings or credential mutation, or
  project-open escape reachable on the loopback HTTP API.
- **Path escape** — reads or writes outside an approved project root via `..`,
  symlinks, or absolute paths that bypass `SandboxBoundary`.
- **Command / argument injection** — through verify, scanner, git, or MCP subprocess
  paths.
- **SSRF or egress abuse** — outbound web research or `fetch_url` reaching internal /
  metadata endpoints past the `guardedGet` and provider-endpoint guards.
- **Secret exposure** — credentials or tokens leaking into logs, tool output, API
  responses, or transcripts.
- **The paintedwolf.ai website** — cross-site scripting, unsafe headers or redirects,
  tampered downloads, or a deployment path an outsider can write to.

Explicitly **out of scope** for the local-first threat model (see
[`docs/security.md`](docs/security.md#local-threat-model)): multi-tenant hosted auth, per-session
container/microVM isolation, encryption-at-rest for SQLite, and any exposure that only
arises after the bind address is deliberately widened off loopback (which requires TLS +
authenticated clients before it ships).

Some limits of the egress boundary are declared rather than defects, and
[`docs/security.md`](docs/security.md) § Egress states each one with the test that pins it.
Two come up often: mediation carries TCP only, so UDP reaches the network only under an
approved direct-IP action; and a direct-IP action's declared destinations bound protocol
and port, never the remote host, because Seatbelt accepts no host but `*` or `localhost` in
a network address. A report that one of these behaves as documented is not a vulnerability.
A report that one of them can be **crossed** — UDP leaving under a mediated mode, a narrowed
action speaking an undeclared transport, or the system resolver socket reachable outside
direct IP — is in scope above.

## Agent misbehaviour is not automatically a vulnerability

The dividing line for *this* document is the **boundary**, not the outcome.

- The agent got **outside** the permissions it was granted — read or wrote outside an approved
  project root, escaped its confinement, reached a host it should not have reached, or put a
  secret somewhere readable: that is in scope above. Private advisory, never a public issue.
- The agent stayed **inside** those permissions and still did something you did not want —
  edited a file you did not ask about, made a bad call, ignored instructions, looped: that is
  not a vulnerability.
- An approved capability did what its approval card said: a command the person let run
  outside the sandbox, a local service such as a container engine acting through an approved
  socket, or direct network access reaching a destination the host does not observe. Those
  are permissions granted, not escapes. A command that runs outside the sandbox, or with any
  of that reach, **without** its approval is in scope above.

That second bullet is the only security guarantee here. It is not a promise that every
in-bounds mistake is a product defect. You can attach any model, and model output is not
deterministic. The host enforces the boundary, injects the prompts, offers the tools, raises
the gates, and implements the loop and stall detection it claims. It does not determine the
next token.

In-bounds reports belong in a public issue only when you think the **host** should have
prevented the action, or when a shipped default consistently fails. A single bad judgment
from a model you chose is expected. That bar, and the issue form, live in
[`REPORTING.md`](REPORTING.md).

If you are unsure which you have, use the private advisory flow.

## Handling of untrusted content

Reading adversary-controlled content (web pages, MCP tool output, repository files) is
the product, not a vulnerability. The relevant question is whether such content can
drive a **consequential action** without a human-visible gate. See
[`docs/security.md`](docs/security.md) § Untrusted content (inbound) for the containment
model and its honest limits before reporting prompt-injection findings.
