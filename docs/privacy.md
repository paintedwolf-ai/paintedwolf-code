# Privacy and your data

What Painted Wolf Code does with your data: what it never collects, everything it can send over the network, where it keeps things on disk, and how to remove all of it.

This describes how the app behaves. It is not a Terms of Service.

## We collect no telemetry

**No analytics. No crash reporting. No usage beacons. No third-party analytics or error SDKs.**

So when something breaks, we have no idea unless you tell us. **Settings → Advanced → Diagnostics** has System information you can copy and a diagnostics bundle you can save — both stay on your machine unless you share them with a report yourself. Reports are welcome; there is no support contract.

## What leaves your machine

Everything the app and the agent can send, and when.

| What | When | Where it goes |
|------|------|---------------|
| **Your prompts and code context** | You send a message, or the agent works on a turn. Also without a turn: a File summary for the file you have open (on by default; the **File summaries** switch in Settings → General → Display turns it off), a display name for a new project from its first prompt, and compaction of a long chat's history | The AI provider you configured, using the stored key, ambient cloud credentials, or keyless local endpoint selected for that instance. This is the main one: your code goes to whichever provider you picked, under that provider's terms |
| **Programs the agent runs** | A command, terminal, or background job the agent starts, or a local MCP server you added, reaches the network | Whatever host the program dials. Proxy-aware traffic crosses the host broker, which records each destination and holds new ones for approval according to your approval posture; a recognized package download-and-run or tool install reaches only its package manager's registry and download hosts. A command you approve with `host_execution` runs outside the sandbox, and its network traffic is not observed |
| **Requests the agent makes** | The agent uses `http_request` during a session, or a workflow you started polls a URL it declared as a wake condition | The host the agent or the workflow named. This is not limited to fetching pages: the agent composes the method, headers, and body, so whatever it puts in a request goes to that host, subject to the outbound secret screen. Cookies are sent only for a jar the agent names |
| **Web searches** | The agent runs a search during a session | Searches fan out in parallel, not to one engine. Direct search is on by default and carries **44 bundled keyless providers** (Wikipedia, Stack Exchange, GitHub, package registries, MDN and similar; the list is [`web-research-providers.yaml`](../lycaon/config/packs/painted-wolf/web-research/host/web-research-providers.yaml)). 31 are queried alongside your own providers; 13 are crawl seeds Direct probes. Any provider you add a key for is queried too. Turning off Direct search drops the bundled set, and **Allow web research** (Settings → Web research) turns all of it off |
| **Model catalog** | Background refresh, or when you refresh in Settings | `models.dev` — a public list of models and their capabilities. Carries nothing about you |
| **Provider model lists** | The app builds its list of assignable models — configured providers are loaded or their cached list expires — and when you refresh or test a provider in Settings. Not tied to a chat turn | The provider itself, authenticated the way its chat requests are — the key you stored, or ambient cloud credentials. Asking a provider which models it offers means proving who is asking, so this is a credentialed request outside any chat turn. Only the account behind those credentials is disclosed; no prompt, project, or file content is sent. A provider you have not configured is never contacted, and a local endpoint stays local |
| **Update check** | On by default, off in Settings → General → Updates | One static manifest for your release channel on `downloads.paintedwolf.dev`. A plain GET: no identifier, no version history, no query parameters. Turning it off stops automatic checks; Check now and installing an update still request the manifest |
| **Remote MCP providers** | Only remote providers you add and enable | The endpoint you configured, plus its sign-in host when needed |
| **Browser engine download** | Provisioning the browser tools outside the packaged app | Google's Chrome for Testing storage. The packaged app ships the browser and never downloads one |
| **Decision checkpoint download** | Provisioning the decision engine outside the packaged app: a development sidecar at first launch | `huggingface.co`, which hands the large files to Hugging Face's download CDN. A plain GET for the checkpoint at a pinned revision, every file checked against a SHA-256 digest pinned in the engine. The packaged app ships the checkpoint and never downloads one |
| **Extension packs** | You install or update a pack from a git URL | The git remote you named. Local packs need no network |
| **Pricing feed** | Cost tracking is on and you selected a network pricing source (the default source reuses the model catalog and makes no extra request) | The feed you selected — LiteLLM's public price table on GitHub, or `ai-pricing.fyi`. Public rate cards; nothing about you or your usage is sent |
| **Vulnerability advisories** | A dependency scan runs | `osv-vulnerabilities.storage.googleapis.com` — the public OSV advisory data, downloaded and matched locally. Your dependency list is never uploaded |
| **Package identity preflight** | The agent proposes a high-risk download-and-run or tool-install action | `api.deps.dev` receives only the package ecosystem, package name, and requested version needed to resolve registry age and source identity. Project files, prompts, other command arguments, and credentials are not sent |

The app's own network clients serve thirteen purposes, the closed inventory in [`internal/egressclass`](../lycaon/internal/egressclass), bound at each app network constructor; HTTP callers must name a registered purpose with its timeout profile, and the git, OSV-library, and updater adapters are checked against the same inventory. Programs the agent runs are not app clients: their network access is governed by the sandbox and approvals described in [Security § Egress](security.md#egress).

The update server sees the connection's source IP. Gradual rollout of new releases is decided on-device from a locally stored random draw; nothing about it is transmitted.

## What stays on your machine

| What | Where |
|------|-------|
| Sessions, transcripts, and history | `~/.config/paintedwolf/store.db` |
| Uploaded attachment bodies | `~/.config/paintedwolf/projects/{project_id}/prompt-attachments/` |
| Settings and preferences | `~/.config/paintedwolf` — the single config and data root |
| API keys, web research credentials, MCP sign-in tokens, and managed-secret values | `{configdir}/credential-vault.age`, an age-encrypted `0600` vault. macOS keeps its random identity device-only in Keychain. Linux and Windows store a scrypt-wrapped identity in `credential-vault-identity.age` and unlock it with the app password. Value-free managed-secret metadata remains in `store.db`; use and reveal records never contain the value or proof. |
| Local API token | `~/.config/paintedwolf/api.token`, `0600` |
| Host identity key | `~/.config/paintedwolf/host-identity.pem`, `0600`. It never leaves the device; only its public key and derived host id are served to authenticated local clients. |
| Per-project settings and overlays | `.paintedwolf/` inside each project folder |

Managed keys are stored outside project folders and excluded from backups and diagnostics. Conversation and retained project content is preserved without redaction in backups, so secrets pasted into that content can be included. Managed references are safe identifiers and may appear in transcripts and receipts; they cannot be resolved outside the associated chat or project through a supported host boundary.

Remote images in chat Markdown are never fetched inside the app; they render as a destination placeholder, and opening one uses the normal external-link confirmation and the selected browser. HTTP debug capture structurally redacts request and response bodies: declared credential-bearing request values are scrubbed before truncation, and malformed credential bodies use a redacted marker instead of raw bytes.

## The local decision model

A small model runs on your machine to decide which tools and instructions each turn needs. It sends nothing over the network. Its receipts, which hold the request text it read and its answers, stay in `store.db` with the rest of your history. Its decision heads are trained on coding-agent sessions coordinated by various models and scored by an LLM judge; Painted Wolf Code never uploads your turns or receipts for training. Details: [Decision engine](decision-engine.md).

## Backup, clearing, and uninstall

- **Back up / restore** in **Settings → Advanced → Data**. A backup carries sessions, retained project and source-history bytes, drafts, rewind checkpoints, settings, and application preferences. Managed credential stores are excluded; conversation and project content is retained without redaction. An in-place restore keeps the credentials already on the device; a fresh install asks for them. Restore validates a disk-staged archive, saves a recovery copy, and asks you to restart. Backups record their exact schema identity and supported older baselines upgrade in staging before restore; see [Compatibility § Backup & restore](compatibility.md#backup--restore).
- **Clear rebuildable data** (caches, indexes, downloaded browser, working copies of completed worker changes) in **Settings → Advanced → Cache**. This never touches your sessions or credentials, and a cleared working copy is rebuilt from its sealed record the next time a review or merge needs it.
- **Uninstall** by deleting the app and removing `~/.config/paintedwolf`. Installed with Homebrew? `brew uninstall --zap --cask painted-wolf-code` does both. On macOS, run the bundled `pw credentials purge --yes` before a manual uninstall to remove the vault identity from Keychain; deleting only the config directory leaves that identity behind. Linux and Windows keep both encrypted files in the config directory. Painted Wolf Code keeps no server copy of your data; content already sent to your AI provider, search providers, or MCP servers stays with them under their terms.

## See also

- [Security](security.md) — what the agent can reach when it works on a project, and the approvals, sandboxing, and untrusted-content handling that bound it
- [Secrets and redaction](secrets.md) — protected values, references, outbound screening, durable redaction, and authenticated reveal
- [Compatibility](compatibility.md) — what survives an upgrade
