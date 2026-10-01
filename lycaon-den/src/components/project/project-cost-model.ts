import type {
  CostSummary,
  ProjectCostReport,
} from "../../api/types.ts";
import {
  hostMeasuredTokens,
  priced,
  unreportedChargedCalls,
  unreportedFreeCalls,
} from "../../cost/cost-format.ts";
import { sumTokens, type CostRole } from "../../cost/cost-roles.ts";

export type ProjectCostSort = "cost" | "activity" | "tokens";

export function totalTokens(summary: CostSummary): number {
  return sumTokens(summary.token_totals);
}

/** True when usage bars use USD. */
export function costUsesUsdBasis(summary: CostSummary): boolean {
  return priced(summary) && summary.estimated_nano_usd > 0;
}

export type CostCompositionBasis = "usd" | "tokens";

export function rolePercent(
  role: CostRole,
  summary: CostSummary,
  basis: CostCompositionBasis,
): number {
  const total = basis === "usd" ? summary.estimated_nano_usd : totalTokens(summary);
  const value = basis === "usd" ? role.nano_usd : sumTokens(role.totals);
  return total > 0 ? (value / total) * 100 : 0;
}

export function hasCostUsage(summary: CostSummary): boolean {
  return (
    summary.estimated_nano_usd > 0 ||
    totalTokens(summary) > 0 ||
    (summary.unknown_calls ?? 0) > 0
  );
}

function csvCell(value: string | number): string {
  const raw = String(value);
  const text = typeof value === "string" && /^[\s\uFEFF]*[=+\-@]/u.test(raw)
    ? `'${raw}`
    : raw;
  return /[",\n]/.test(text) ? `"${text.replaceAll('"', '""')}"` : text;
}

function pricingSources(summary: CostSummary): string {
  return summary.pricing_provenance.map((entry) => entry.source).join("; ");
}

function pricingTimes(summary: CostSummary): string {
  return summary.pricing_provenance
    .map((entry) => entry.priced_at ?? "")
    .join("; ");
}

function unpricedTokenCount(summary: CostSummary): number {
  return summary.unpriced_tokens ?? (priced(summary) ? 0 : totalTokens(summary));
}

/** The summary columns shared by session, utility, and retired rows. */
function summaryCells(cost: CostSummary): (string | number)[] {
  return [
    cost.estimate_coverage,
    priced(cost) ? cost.estimated_nano_usd / 1e9 : "",
    unpricedTokenCount(cost),
    unreportedChargedCalls(cost),
    unreportedFreeCalls(cost),
    hostMeasuredTokens(cost),
    pricingSources(cost),
    pricingTimes(cost),
    cost.token_totals.prompt,
    cost.token_totals.completion,
    cost.token_totals.cache_read ?? 0,
    cost.token_totals.cache_write ?? 0,
    cost.cache_savings ? cost.cache_savings.estimated_nano_usd / 1e9 : "",
    cost.cache_savings?.unpriced_tokens ?? 0,
  ];
}

export function buildProjectCostCSV(
  report: Pick<ProjectCostReport, "summary" | "project_utilities" | "retired_sessions" | "sessions">,
  projectName: string,
): string {
  const header = [
    "Project",
    "Session",
    "Status",
    "Archived",
    "Last activity",
    "Estimate coverage",
    "Estimated USD",
    "Unpriced tokens",
    "Unreported charged calls",
    "Unreported local calls",
    "Host-measured tokens",
    "Pricing sources",
    "Pricing timestamps",
    "Prompt tokens",
    "Completion tokens",
    "Cache-read tokens",
    "Cache-write tokens",
    "Estimated cache comparison USD",
    "Cache tokens without comparison price",
    "Worker tasks",
  ];
  const line = (cells: (string | number)[]): string => cells.map(csvCell).join(",");
  const lines = report.sessions.map((row) =>
    line([
      projectName,
      row.session.title?.trim() || "Untitled session",
      row.session.status,
      row.session.archived_at ? "Yes" : "No",
      row.session.activity_at,
      ...summaryCells(row.cost),
      row.cost.workers.task_count ?? 0,
    ]),
  );
  if (hasCostUsage(report.project_utilities)) {
    lines.push(
      line([
        projectName,
        "Project utilities",
        "Outside a session",
        "No",
        "",
        ...summaryCells(report.project_utilities),
        0,
      ]),
    );
  }
  if (hasCostUsage(report.retired_sessions)) {
    lines.push(
      line([
        projectName,
        "Deleted or expired sessions",
        "Retired",
        "Yes",
        "",
        ...summaryCells(report.retired_sessions),
        report.retired_sessions.workers.task_count ?? 0,
      ]),
    );
  }
  return [header.join(","), ...lines].join("\n");
}
