import { describe, expect, it } from "vitest";
import { gateLabel } from "./gate-copy.ts";
import { GATE_LABELS } from "./gate-copy.generated.ts";

describe("gateLabel", () => {
  it("says what was true rather than what might go wrong", () => {
    expect(gateLabel("sensitive_location")).toBe(
      "This is a sensitive location outside your project",
    );
    expect(gateLabel("secret_outbound")).toBe(
      "A credential was about to leave this machine",
    );
    // No label reads as a verdict on the action; the card's consequence row carries that.
    for (const label of Object.values(GATE_LABELS)) {
      expect(label).not.toMatch(/danger|malicious|attack/i);
    }
  });

  it("renders nothing for an absent gate", () => {
    // `gate` is optional on the wire; Record<ApprovalGate, string> types the present ones.
    expect(gateLabel(undefined)).toBe("");
  });
});
