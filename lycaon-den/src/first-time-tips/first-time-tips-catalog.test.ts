import { describe, expect, it } from "vitest";
import {
  FIRST_TIME_TIPS,
  FIRST_TIME_TIP_ROLLOUT,
} from "./first-time-tips-catalog.ts";

describe("first-time tips catalog", () => {
  it("derives one globally ordered rollout from unique catalog priorities", () => {
    const priorities = FIRST_TIME_TIP_ROLLOUT.map(
      (id) => FIRST_TIME_TIPS[id].priority,
    );

    expect(new Set(FIRST_TIME_TIP_ROLLOUT).size).toBe(
      Object.keys(FIRST_TIME_TIPS).length,
    );
    expect(new Set(priorities).size).toBe(priorities.length);
    expect(priorities).toEqual([...priorities].sort((left, right) => left - right));
  });

  it("keeps workflow guidance beside the rail instead of over workflow actions", () => {
    expect(FIRST_TIME_TIPS.workflows.preferredPlacement).toBe("right");
  });
});
