import { describe, expect, it } from "vitest";
import { workflowObligationSuffix, workflowTopologySuffix } from "./workflow-phase-labels.ts";

describe("workflowObligationSuffix", () => {
  it("is empty with no obligations or none pending", () => {
    expect(workflowObligationSuffix(undefined)).toBe("");
    expect(workflowObligationSuffix([])).toBe("");
    expect(workflowObligationSuffix([{ kind: "scan", status: "complete" }])).toBe("");
    expect(workflowObligationSuffix([{ kind: "scan", status: "failed" }])).toBe("");
  });

  it("names the warming hold before engine progress", () => {
    expect(
      workflowObligationSuffix([
        { kind: "scan", status: "pending", detail: { warming: true, engines_pending: ["gitleaks"] } },
      ]),
    ).toBe(" · warming source snapshot");
  });

  it("lists pending engines", () => {
    expect(
      workflowObligationSuffix([
        { kind: "scan", status: "pending", detail: { engines_pending: ["gitleaks", "opengrep"] } },
      ]),
    ).toBe(" · running gitleaks, opengrep");
  });

  it("falls back to scan counts, then a generic scan wait", () => {
    expect(
      workflowObligationSuffix([
        { kind: "scan", status: "pending", detail: { scans_total: 3, scans_terminal: 1 } },
      ]),
    ).toBe(" · 1/3 scans done");
    expect(workflowObligationSuffix([{ kind: "scan", status: "pending" }])).toBe(
      " · waiting on scanners",
    );
  });

  it("names unknown kinds generically", () => {
    expect(workflowObligationSuffix([{ kind: "index", status: "pending" }])).toBe(
      " · waiting on index",
    );
  });
});

describe("workflowTopologySuffix", () => {
  it("counts the finished workers of the current phase only", () => {
    const legs = [
      { id: "a", stage: "a", phase_id: "hunt", label: "A", status: "complete" as const },
      { id: "b", stage: "b", phase_id: "hunt", label: "B", status: "running" as const },
      { id: "c", stage: "c", phase_id: "triage", label: "C", status: "pending" as const },
    ];
    expect(workflowTopologySuffix(legs, "hunt")).toBe(" · 1 of 2 workers done");
    expect(workflowTopologySuffix(legs, "judge")).toBe("");
    expect(workflowTopologySuffix(undefined, "hunt")).toBe("");
  });
});
