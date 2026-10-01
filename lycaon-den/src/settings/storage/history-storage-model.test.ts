import { describe, expect, it } from "vitest";
import { historyLaneUsage } from "./history-storage-model.ts";

describe("history storage measurements", () => {
  it("keeps recovery snapshot lengths separate from physical allocation", () => {
    const usage = historyLaneUsage({ id: "upgrade-recovery", stored_bytes: 0, logical_bytes: 1024 ** 3, shared_bytes: 0 });
    expect(usage).toBe("1.0 GiB retained snapshot content · may share disk space with live files and other snapshots");
    expect(usage).not.toContain("0 B stored");
  });

  it("counts shared compressed content once", () => {
    expect(historyLaneUsage({ id: "checkpoint_content", stored_bytes: 1024, logical_bytes: 2048, shared_bytes: 1024 }))
      .toBe("1.0 KiB stored · 2.0 KiB uncompressed · 1.0 KiB shared");
  });
});
