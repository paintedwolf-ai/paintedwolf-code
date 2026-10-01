import { describe, expect, it } from "vitest";
import {
  clearPendingArmWorkflow,
  peekArmWorkflow,
  requestArmWorkflow,
} from "./pending-arm.ts";

describe("pending-arm", () => {
  it("peeks without clearing until clearPendingArmWorkflow", () => {
    clearPendingArmWorkflow();
    requestArmWorkflow({
      sessionId: "sess-1",
      workflow: { id: "plan", version: "1.0.0", name: "Plan", trigger: "/plan" },
    });
    expect(peekArmWorkflow("sess-other")).toBeNull();
    expect(peekArmWorkflow("sess-1")).toEqual({
      id: "plan",
      version: "1.0.0",
      name: "Plan",
      trigger: "/plan",
    });
    expect(peekArmWorkflow("sess-1")).toEqual({
      id: "plan",
      version: "1.0.0",
      name: "Plan",
      trigger: "/plan",
    });
    clearPendingArmWorkflow();
    expect(peekArmWorkflow("sess-1")).toBeNull();
  });
});
