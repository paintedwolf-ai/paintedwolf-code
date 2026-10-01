import { describe, expect, it } from "vitest";
import type { ToolPartView } from "../tool/tool-part-model.ts";
import {
  isVerdictToolPart,
  verdictOutcomeFromPart,
  verdictStatusLine,
} from "./verdict-card-model.ts";

const base: ToolPartView = {
  id: "a1:call_1",
  toolCallId: "call_1",
  assistantMessageId: "a1",
  messageId: "t1",
  tool: "submit_verdict",
  kind: "generic",
  status: "completed",
};

describe("verdict card model", () => {
  it("routes only rows carrying a recorded verdict", () => {
    expect(isVerdictToolPart(base)).toBe(false);
    expect(isVerdictToolPart({ ...base, verdict: { verdict: " " } })).toBe(false);
    expect(isVerdictToolPart({ ...base, verdict: { verdict: "CHALLENGED" } })).toBe(true);
    expect(
      verdictOutcomeFromPart({ ...base, verdict: { verdict: "CHALLENGED" } })?.verdict,
    ).toBe("CHALLENGED");
  });

  it("summarizes terminal and re-loop outcomes", () => {
    expect(
      verdictStatusLine({ verdict: "CHALLENGED", terminal: true, evidence_key: "survey_challenged" }),
    ).toBe("Terminal — evidence_passed:survey_challenged satisfied");
    expect(verdictStatusLine({ verdict: "CLAIMED", terminal: true })).toBe(
      "Terminal — the host is advancing the phase",
    );
    expect(
      verdictStatusLine({ verdict: "NEEDS_REVISION", attempt: 1, iteration_cap: 2 }),
    ).toBe("Non-terminal — attempt 1 of 2");
    expect(verdictStatusLine({ verdict: "NEEDS_REVISION" })).toBe(
      "Non-terminal — the review re-loops",
    );
  });
});
