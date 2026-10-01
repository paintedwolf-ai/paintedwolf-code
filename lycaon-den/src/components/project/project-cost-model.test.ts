import { describe, expect, it } from "vitest";
import type { CostSummary, ProjectCostSession } from "../../api/types.ts";
import { costRoles } from "../../cost/cost-roles.ts";
import {
  buildProjectCostCSV,
  costUsesUsdBasis,
  hasCostUsage,
  rolePercent,
  totalTokens,
} from "./project-cost-model.ts";

const cost = (usd: number, prompt: number, completion: number): CostSummary => ({
  scope: "session",
  estimated_nano_usd: Math.round(usd * 1e9),
  estimate_coverage: "complete",
  pricing_provenance: [],
  token_totals: { prompt, completion },
  coordinator: {
    estimated_nano_usd: Math.round(usd * 0.6 * 1e9),
    token_totals: { prompt: prompt * 0.6, completion: completion * 0.6 },
  },
  workers: {
    estimated_nano_usd: Math.round(usd * 0.4 * 1e9),
    token_totals: { prompt: prompt * 0.4, completion: completion * 0.4 },
    task_count: 2,
  },
  summarizer: {
    estimated_nano_usd: 0,
    token_totals: { prompt: 0, completion: 0 },
  },
});

const row = (
  id: string,
  title: string,
  usd: number,
  updatedAt: string,
): ProjectCostSession => ({
  session: {
    id,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "project-1",
    title,
    posture: "build",
    status: "idle",
    message_count: 3,
    created_at: updatedAt,
    activity_at: updatedAt,
  },
  cost: cost(usd, usd * 100, usd * 20),
});

describe("project cost model", () => {
  const sessions = [
    row("a", "Alpha", 1, "2026-01-01T00:00:00Z"),
    row("b", "Beta", 3, "2026-02-01T00:00:00Z"),
  ];

  it("builds role shares and totals", () => {
    const summary = cost(10, 100, 25);
    expect(totalTokens(summary)).toBe(125);
    expect(rolePercent(costRoles(summary)[0]!, summary, "usd")).toBe(60);
    expect(rolePercent(costRoles(summary)[0]!, summary, "tokens")).toBe(60);
    expect(costRoles(summary)[2]?.label).toBe("Summarizer");
    expect(costRoles(summary).map((role) => role.id)).toEqual([
      "coordinator",
      "workers",
      "summarizer",
    ]);
    expect(costUsesUsdBasis(summary)).toBe(true);
    const free = { ...summary, estimated_nano_usd: 0 };
    expect(costUsesUsdBasis(free)).toBe(false);
    expect(rolePercent(costRoles(free)[0]!, free, "tokens")).toBe(60);
    expect(rolePercent(costRoles(free)[0]!, free, "usd")).toBe(0);
  });

  it("exports one auditable row per session", () => {
    const partialSessions = sessions.map((session, index) =>
      index === 0
        ? {
            ...session,
            cost: {
              ...session.cost,
              estimate_coverage: "lower_bound" as const,
              unpriced_tokens: 12,
              pricing_provenance: [
                {
                  source: "models-dev",
                  priced_at: "2026-01-02T00:00:00Z",
                },
              ],
            },
          }
        : session,
    );
    const csv = buildProjectCostCSV(
      {
        summary: { ...cost(4.75, 475, 95), scope: "project" },
        project_utilities: { ...cost(0.5, 50, 10), scope: "project" },
        retired_sessions: { ...cost(0.25, 25, 5), scope: "project" },
        sessions: partialSessions,
      },
      'Demo, "one"',
    );
    expect(csv).toContain('"Demo, ""one"""');
    expect(csv).toContain("Alpha");
    expect(csv).toContain("Project utilities,Outside a session");
    expect(csv).toContain(
      "Estimate coverage,Estimated USD,Unpriced tokens,Unreported charged calls,Unreported local calls,Host-measured tokens,Pricing sources,Pricing timestamps",
    );
    expect(csv).toContain("lower_bound,1,12,0,0,0,models-dev,2026-01-02T00:00:00Z");
    expect(csv).toContain("Deleted or expired sessions,Retired");
    expect(csv.split("\n")).toHaveLength(5);
    expect(
      hasCostUsage({ ...cost(0, 0, 0), scope: "project" }),
    ).toBe(false);
  });

  it("exports the host coverage and the count when provider calls went unreported", () => {
    const unreported = {
      ...sessions[0]!,
      cost: {
        ...sessions[0]!.cost,
        estimate_coverage: "lower_bound" as const,
        unknown_calls: 2,
        unknown_charged_calls: 2,
      },
    };
    const csv = buildProjectCostCSV(
      {
        summary: { ...cost(1, 100, 20), scope: "project" },
        project_utilities: { ...cost(0, 0, 0), scope: "project" },
        retired_sessions: { ...cost(0, 0, 0), scope: "project" },
        sessions: [unreported],
      },
      "Demo",
    );
    expect(csv).toContain("lower_bound,1,0,2,0,0,");
  });

  it("exports cache buckets and signed comparison coverage independently of spend", () => {
    const session = row("cache", "Cache", 1.25, "2026-01-01T00:00:00Z");
    session.cost.token_totals = { prompt: 1000, completion: 20, cache_read: 200, cache_write: 500 };
    session.cost.cache_savings = { estimated_nano_usd: -125_000_000, unpriced_tokens: 200 };
    const lines = buildProjectCostCSV({
      summary: { ...session.cost, scope: "project" },
      project_utilities: { ...cost(0, 0, 0), scope: "project" },
      retired_sessions: { ...cost(0, 0, 0), scope: "project" },
      sessions: [session],
    }, "Demo").split("\n");
    const headings = lines[0]!.split(",");
    const cells = lines[1]!.split(",");
    const value = (heading: string) => cells[headings.indexOf(heading)];
    expect(cells).toHaveLength(headings.length);
    expect(value("Estimated USD")).toBe("1.25");
    expect(value("Prompt tokens")).toBe("1000");
    expect(value("Cache-read tokens")).toBe("200");
    expect(value("Cache-write tokens")).toBe("500");
    expect(value("Estimated cache comparison USD")).toBe("-0.125");
    expect(value("Cache tokens without comparison price")).toBe("200");
  });

  it("exports free local unreported calls without marking the row partial", () => {
    const localOnly = {
      ...sessions[0]!,
      cost: { ...sessions[0]!.cost, unknown_calls: 2, host_measured_tokens: 40 },
    };
    const csv = buildProjectCostCSV(
      {
        summary: { ...cost(1, 100, 20), scope: "project" },
        project_utilities: { ...cost(0, 0, 0), scope: "project" },
        retired_sessions: { ...cost(0, 0, 0), scope: "project" },
        sessions: [localOnly],
      },
      "Demo",
    );
    expect(csv).toContain("complete,1,0,0,2,40,");
  });

  it("leaves the estimated amount blank when a row is unpriced", () => {
    const unpriced = {
      ...sessions[0]!,
      cost: { ...sessions[0]!.cost, estimate_coverage: "unpriced" as const },
    };
    const csv = buildProjectCostCSV(
      {
        summary: { ...cost(0, 0, 0), scope: "project" },
        project_utilities: { ...cost(0, 0, 0), scope: "project" },
        retired_sessions: { ...cost(0, 0, 0), scope: "project" },
        sessions: [unpriced],
      },
      "Demo",
    );
    expect(csv).toContain("unpriced,,120,0,0,0,,,");
  });

  it("neutralizes spreadsheet formulas in every string cell", () => {
    const csv = buildProjectCostCSV(
      {
        summary: { ...cost(1, 100, 20), scope: "project" },
        project_utilities: { ...cost(0, 0, 0), scope: "project" },
        retired_sessions: { ...cost(0, 0, 0), scope: "project" },
        sessions: [row("formula", "=HYPERLINK(\"https://example.test\")", 1, "2026-01-01T00:00:00Z")],
      },
      "+SUM(A1:A2)",
    );

    expect(csv).toContain("'+SUM(A1:A2)");
    expect(csv).toContain("'=HYPERLINK");
    expect(csv).not.toContain(",=HYPERLINK");
  });

  it("treats an unreported call as usage even without tokens or a price", () => {
    expect(
      hasCostUsage({
        ...cost(0, 0, 0),
        scope: "project",
        unknown_calls: 1,
      }),
    ).toBe(true);
  });
});
