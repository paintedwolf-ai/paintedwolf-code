Keep the *reason to update* separate from the *cost of updating*. Conflating them leads to
chasing expensive presentation upgrades for no benefit, or to neglecting security engines
because the migration is hard.

### Update urgency

* **Critical:** Direct exposure to untrusted input, a sandbox or confinement boundary, or
  credential cryptography. Examples: the bundled headless browser, the Git engine, the vault
  cipher, the HTML sanitizer, and the vulnerability database behind the offline gate.
* **High:** Fast-moving external protocols, core host persistence, and parsers of untrusted
  content. Examples: MCP, LLM provider wire APIs, Go standard library point releases, SQLite,
  and CRDT state.
* **Moderate:** Tooling reliability, test frameworks, and non-security upstream fixes.
* **Low:** Local presentation or developer-only components with no network exposure, no code
  evaluation, and no privilege path. Leave these pinned until a concrete defect or constraint
  requires a change.

### Update friction

* **High:** The update rebases a local patch, adopts a breaking redesign, or coordinates
  runtimes, as with Yrs in Go-hosted WASM paired with Yjs in Den. Platform signing,
  per-platform hashes, and size budgets also count here.
* **Moderate:** API deprecations, regenerated output that needs review, or verification that no
  automated gate fully covers, such as Seatbelt confinement or the Windows shell.
* **Low:** A semver-compatible bump that `./task check-fast` or `./task check` fully verifies.
