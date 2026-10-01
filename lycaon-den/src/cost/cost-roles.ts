import type { CostSummary, TokenTotals } from "../api/types.ts";

export type CostRole = {
  id: "coordinator" | "workers" | "summarizer";
  label: string;
  nano_usd: number;
  totals: TokenTotals;
};

export function sumTokens(totals: TokenTotals): number {
  return totals.prompt + totals.completion;
}

export function costRoles(summary: CostSummary): CostRole[] {
  return [
    {
      id: "coordinator",
      label: "Coordinator",
      nano_usd: summary.coordinator.estimated_nano_usd,
      totals: summary.coordinator.token_totals,
    },
    {
      id: "workers",
      label: "Workers",
      nano_usd: summary.workers.estimated_nano_usd,
      totals: summary.workers.token_totals,
    },
    {
      id: "summarizer",
      label: "Summarizer",
      nano_usd: summary.summarizer.estimated_nano_usd,
      totals: summary.summarizer.token_totals,
    },
  ];
}
