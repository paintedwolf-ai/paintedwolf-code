import { describe, expect, it } from "vitest";
import type { WebResearchWarmActivity } from "../../api/types.ts";
import { webResearchActivityToDenseItems } from "./web-research-activity.ts";

describe("webResearchActivityToDenseItems", () => {
  it("maps trigger, tier, pages, topic, skip reason, and relative time", () => {
    const activity: WebResearchWarmActivity[] = [
      {
        at: "2026-07-22T12:00:00Z",
        trigger: "session_tick",
        tier: "seed",
        topic: "solidjs",
        pages: 3,
        skip_reason: "busy",
      },
      {
        at: "2026-07-22T11:00:00Z",
        trigger: "search_seed",
      },
    ];
    const now = Date.parse("2026-07-22T12:30:00Z");
    const items = webResearchActivityToDenseItems(activity, now);
    expect(items).toHaveLength(2);
    expect(items[0]).toMatchObject({
      primary: "session_tick · seed",
      secondary: "3 pages · solidjs · busy",
      meta: "30m ago",
    });
    expect(items[0]?.metaTitle).toContain("2026");
    expect(items[1]).toMatchObject({
      primary: "search_seed",
      secondary: undefined,
      meta: "1h ago",
    });
  });
});
