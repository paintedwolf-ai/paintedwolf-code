import { describe, expect, it } from "vitest";
import { approvalOptionFixture } from "./approval-test-fixtures.ts";
import { APPROVAL_SLOT_COUNT, approvalSlot, optionForSlot } from "./approval-slots.ts";

describe("approval slots", () => {
  it("assigns each rung its fixed digit and grouped options none", () => {
    expect(APPROVAL_SLOT_COUNT).toBe(4);
    expect(approvalSlot({ rung: "once" })).toBe(1);
    expect(approvalSlot({ rung: "unchanged" })).toBe(1);
    expect(approvalSlot({ rung: "tracked" })).toBe(1);
    expect(approvalSlot({ rung: "redacted" })).toBeUndefined();
    expect(approvalSlot({ rung: "day" })).toBe(2);
    expect(approvalSlot({ rung: "chat" })).toBe(3);
    expect(approvalSlot({ rung: "project" })).toBe(4);
    expect(approvalSlot({ rung: "device" })).toBe(4);
    expect(approvalSlot({ rung: "project", group: "Also allow" })).toBeUndefined();
    expect(approvalSlot({ rung: "chat", group: "Allow and stop asking" })).toBeUndefined();
  });

  it("picks only the ungrouped, enabled option for a digit", () => {
    const options = [
      approvalOptionFixture({ id: "once", rung: "once" }),
      approvalOptionFixture({ id: "project", rung: "project", kind: "lease", disabled: true, note: "no project" }),
      approvalOptionFixture({ id: "trust", rung: "device", kind: "lease", group: "Trust" }),
      approvalOptionFixture({ id: "chat", rung: "chat", kind: "lease" }),
    ];
    expect(optionForSlot(options, 1)?.id).toBe("once");
    expect(optionForSlot(options, 3)?.id).toBe("chat");
    // A disabled rung stays disabled; the grouped device option never answers its digit.
    expect(optionForSlot(options, 4)).toBeUndefined();
    expect(optionForSlot(options, 2)).toBeUndefined();
  });
});
