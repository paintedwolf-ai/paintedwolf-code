import { describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
import { toolPartFromCall } from "./tool-part-model.ts";
import { buildStructuredToolPresentation } from "./tool-part-structured.ts";

function verification(body: Record<string, unknown>) {
  const message: Message = {
    id: "verify-result", role: "tool", origin: "tool", authority: "none",
    trust_tier: "untrusted", created_at: "t",
    tool_result: { content: JSON.stringify(body), outcome: "completed" },
  };
  return toolPartFromCall({ id: "verify-call", name: "verify", args: {} }, message, "assistant");
}

describe("verification result presentation", () => {
  it.each(["passed", "failed", "unverifiable"])("preserves versioned %s evidence and output", (outcome) => {
    const part = verification({ result_version: 2, outcome, state: "completed", status: 0, stdout: "check output", stderr: "diagnostic" });
    const view = buildStructuredToolPresentation(part);
    const facts = view.sections.flatMap((section) => section.kind === "facts" ? section.facts : []);
    expect(facts).toEqual(expect.arrayContaining([
      { label: "Result version", value: "2" },
      { label: "Outcome", value: outcome },
      { label: "State", value: "completed" },
    ]));
    expect(facts.some((fact) => fact.label === "Verification")).toBe(false);
    expect(JSON.stringify(view.sections)).toContain("check output");
    expect(JSON.stringify(view.sections)).toContain("diagnostic");
  });

  it("does not invent a verdict for an unsettled program", () => {
    const view = buildStructuredToolPresentation(verification({ result_version: 2, state: "parked", lease_id: "lease", reason: "sleep 120" }));
    const facts = view.sections.flatMap((section) => section.kind === "facts" ? section.facts : []);
    expect(facts.some((fact) => fact.label === "Outcome" || fact.label === "Verification")).toBe(false);
  });

  it("keeps historical confirmation without assigning a new result version", () => {
    const view = buildStructuredToolPresentation(verification({ passed: true, confirmed: true, exit_code: 0 }));
    const facts = view.sections.flatMap((section) => section.kind === "facts" ? section.facts : []);
    expect(facts).toContainEqual({ label: "Verification", value: "host-confirmed" });
    expect(facts.some((fact) => fact.label === "Result version")).toBe(false);
  });
});
