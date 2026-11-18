import { describe, expect, it } from "vitest";
import { allCheckpointStatuses, resolvedCheckpointStatuses } from "./enum-registries.ts";

describe("wire enum registries", () => {
  it("resolvedCheckpointStatuses is every status except pending", () => {
    const all = allCheckpointStatuses();
    const resolved = resolvedCheckpointStatuses();
    expect(resolved).not.toContain("pending");
    expect(resolved).toHaveLength(all.length - 1);
    expect(new Set(resolved).size).toBe(resolved.length);
    for (const status of all) {
      if (status === "pending") continue;
      expect(resolved).toContain(status);
    }
  });
});
