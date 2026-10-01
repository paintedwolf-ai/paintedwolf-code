import { describe, expect, it } from "vitest";
import { approvalWaitLabel } from "./approval-wait.ts";

const issued = Date.parse("2026-09-06T10:00:00Z");

describe("approvalWaitLabel", () => {
  it("stays silent under a minute — a card answered in seconds needs no clock", () => {
    expect(approvalWaitLabel("2026-09-06T10:00:00Z", issued + 59_000)).toBe("");
  });

  it("counts minutes, then hours, then days, coarsely", () => {
    expect(approvalWaitLabel("2026-09-06T10:00:00Z", issued + 60_000)).toBe("1 min");
    expect(approvalWaitLabel("2026-09-06T10:00:00Z", issued + 21 * 60_000)).toBe("21 min");
    expect(approvalWaitLabel("2026-09-06T10:00:00Z", issued + 3 * 3_600_000)).toBe("3 h");
    expect(approvalWaitLabel("2026-09-06T10:00:00Z", issued + 50 * 3_600_000)).toBe("2 d");
  });

  it("renders nothing for an unparseable issue time", () => {
    expect(approvalWaitLabel("not a time", issued)).toBe("");
  });
});
