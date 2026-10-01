import { describe, expect, it } from "vitest";
import { dragRequestsHide } from "./drag-to-hide.ts";

describe("drag to hide", () => {
  it("hides below half the floor and keeps the pane from half the floor up", () => {
    expect(dragRequestsHide(103, 208)).toBe(true);
    expect(dragRequestsHide(104, 208)).toBe(false);
    expect(dragRequestsHide(-40, 340)).toBe(true);
    expect(dragRequestsHide(Number.NaN, 208)).toBe(false);
  });
});
