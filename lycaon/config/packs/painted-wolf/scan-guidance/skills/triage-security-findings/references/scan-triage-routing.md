# Scan triage routing reference

Maintained sources of truth: the live scan tool schemas and structured rejection `Code:` guidance. Scanner results are bounded evidence from configured engines, not proof that unreported code is safe.

## Resolve and drill

| Need | Tool | Rule |
|---|---|---|
{% if profile_has_scan_list and root_count %}| Find stored runs | `scan_list` | Use a full returned id; omit project paths the host already knows |{% endif %}
{% if profile_has_scan_summary and root_count %}| Check completion and category totals | `scan_summary` | Summary first; full view only when ingest audit rows matter |{% endif %}
{% if profile_has_scan_query and root_count %}| Inspect findings | `scan_query` | Filter and paginate; retain the scan id with every conclusion |{% endif %}
{% if profile_has_scan_pack and root_count %}| Run configured scanners | `scan_pack` | Use supported categories; completion may be asynchronous |{% endif %}
{% if profile_has_scan_compare and root_count %}| Prove a fix changed scanner state | `scan_compare` | Use the bound comparison pair; choose latest/baseline only when unbound |{% endif %}

Keep workflow-bound scan ids throughout. On `SCAN_NOT_FOUND`, report a missing bound run; {% if profile_has_scan_list and root_count %}use `scan_list` to resolve ids only for unbound work.{% endif %} On incomplete-scan rejection, wait for or report completion rather than querying around it. When the list is empty, distinguish no stored scan from a clean completed scan.

## Adjudicate a finding

1. Read the rule, severity, message, path, line, and scanner provenance.
2. Inspect the bounded source region and the nearest relevant definition or caller.
3. Establish whether input is controlled, validation occurs earlier, the path is reachable, and the reported sink or consequence is real.
4. Classify the example as a confirmed positive, scanner noise, or unresolved with the exact missing fact.
5. Stop sampling a rule when representative results establish its repeated shape; disclose the remaining count.

Do not tune or suppress a scanner merely because a finding is inconvenient. Rule changes and detection-pack authoring are separate tasks.

## Ignore ledger (`.paintedwolf/ignores.yaml`)

Deliberate test fixtures, test canaries, and confirmed false positives are ignored in `.paintedwolf/ignores.yaml` rather than tuning detection rules or suppressing scanners.

```yaml
version: 1
findings:
  - id: <unique-id>
    path: <project-relative-path>
    kind: secret  # optional: secret, sast, or sca
    reason: <why this finding is an expected test fixture or confirmed noise>
```

When proposing an ignore entry:
1. Confirm the finding is deliberate test material or verified false positive from scanner evidence.
2. Edit `.paintedwolf/ignores.yaml` with native write or edit tools.
3. The host routes the edit through agent-policy review (`GateAgentPolicyChange`), presenting the exact diff to the user for approval.
