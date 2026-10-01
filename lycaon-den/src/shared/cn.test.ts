import { describe, expect, it } from "vitest";
import { cn } from "./cn.ts";

describe("cn", () => {
  it("joins truthy parts with spaces", () => {
    expect(cn("a", "b", "c")).toBe("a b c");
  });

  it("skips falsy parts", () => {
    expect(cn("a", false, null, undefined, "b")).toBe("a b");
  });

  it("returns empty string when all parts are falsy", () => {
    expect(cn(false, null, undefined)).toBe("");
  });

  it("conflict-merges Tailwind spacing utilities so the last wins", () => {
    expect(cn("px-8 py-2", "px-4")).toBe("py-2 px-4");
  });

  it("conflict-merges btn-* variant utilities so the last wins", () => {
    expect(cn("btn-primary", "btn-ghost")).toBe("btn-ghost");
  });

  it("lets caller padding override primitive defaults without duplicate px-*", () => {
    expect(cn("btn-primary px-8", "px-4")).toBe("btn-primary px-4");
  });
});
