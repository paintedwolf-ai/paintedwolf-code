import { describe, expect, it, vi } from "vitest";
import { clearWebIndexViaLocalData } from "./local-data-actions.ts";

describe("local-data-actions", () => {
  it("clears web_index via local-data then refreshes index status", async () => {
    const client = {
      clearLocalData: vi.fn().mockResolvedValue({
        results: [{ id: "web_index", ok: true }],
      }),
      getWebResearchIndex: vi.fn().mockResolvedValue({
        available: true,
        docs: 0,
        warming: true,
        activity: [],
      }),
    };

    const status = await clearWebIndexViaLocalData(client as never);
    expect(client.clearLocalData).toHaveBeenCalledWith({ buckets: ["web_index"] });
    expect(client.getWebResearchIndex).toHaveBeenCalled();
    expect(status.docs).toBe(0);
  });
});
