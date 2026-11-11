import { describe, expect, it } from "vitest";
import { gateLabel } from "./gate-copy.ts";

describe("gateLabel", () => {
  it("renders nothing for an absent gate", () => {
    // `gate` is optional on the wire; Record<ApprovalGate, string> types the present ones.
    expect(gateLabel(undefined)).toBe("");
  });
});
