import type { ToolApprovalPayload } from "../../api/types.ts";
import { gateLabel } from "../../chat/checkpoint/gate-copy.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { type DetailRow } from "./ApprovalShell.tsx";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";

export function approvalEvidenceRows(plan: ToolApprovalPayload["plan"] | undefined, declared: string[] | undefined, sockets: string[]): DetailRow[] {
    const copy = APPROVALS_COPY.card;
    const rows: DetailRow[] = [];
    const p = plan?.presentation;
    if (p?.who) rows.push({ label: copy.details.whoLabel, value: p.who });
    if (p?.if_wrong) {
      rows.push({ label: copy.details.ifWrongLabel, value: p.if_wrong });
    }
    if (declared) {
      rows.push({
        label: copy.details.declaredDestinationsLabel,
        value: declared.length > 0 ? declared.join(", ") : copy.details.directIPUndeclared,
      });
      rows.push({
        label: copy.details.directIPVisibilityLabel,
        value: copy.details.directIPUnobserved,
      });
    }
    for (const resolved of sockets) {
      rows.push({
        label: copy.details.resolvedSocketPathLabel,
        value: resolved,
      });
    }
    if (p?.detection) {
      rows.push({
        label: copy.details.flaggedByLabel,
        value: `${p.detection.rule_title} · ${p.detection.pack_id}`,
      });
    }
    for (const [index, rule] of (p?.approval_rules ?? []).entries()) {
      const source = [
        rule.scope === "project" ? "Repository" : rule.scope === "device" ? "Device" : "",
        rule.pack_id,
        rule.unit_id,
      ].filter(Boolean).join(" · ");
      rows.push({
        label: index === 0 ? copy.details.policyLabel : copy.details.alsoPolicyLabel,
        value: `${formatSentenceCase(rule.effect)} ${rule.category}:${rule.pattern}${source ? ` · ${source}` : ""}`,
      });
    }
    const reasons = plan?.reasons ?? [];
    if (p?.gate) rows.push({ label: copy.details.whyLabel, value: gateLabel(p.gate) });
    for (const reason of reasons) {
      if (!reason || reason === p?.gate) continue;
      rows.push({ label: copy.details.alsoLabel, value: gateLabel(reason) });
    }
    for (const fact of p?.cited ?? []) {
      rows.push({
        label: fact.gate
          ? `${copy.details.observedLabel} · ${gateLabel(fact.gate)}`
          : copy.details.observedLabel,
        value: `${fact.key}: ${fact.value} (${fact.source})`,
      });
    }
    if (p?.grant_delta?.trim()) rows.push({label: "Permission change", value: p.grant_delta});
    return rows;
}
