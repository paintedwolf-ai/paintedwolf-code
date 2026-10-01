import { describe, expect, it } from "vitest";
import { prefersReducedMotion } from "./reduced-motion.ts";

describe("prefersReducedMotion", () => {
  it("reads the injected window preference", () => {
    const view = {
      matchMedia: () => ({ matches: true }) as MediaQueryList,
    } as Pick<Window, "matchMedia">;
    expect(prefersReducedMotion(view)).toBe(true);
  });

  it("defaults safely without a media-query implementation", () => {
    expect(prefersReducedMotion(null)).toBe(false);
    expect(
      prefersReducedMotion({
        matchMedia: () => {
          throw new Error("unavailable");
        },
      }),
    ).toBe(false);
  });

  it("queries once per view and reads the live result after", () => {
    let calls = 0;
    const list = { matches: false } as unknown as MediaQueryList;
    const view = {
      matchMedia: () => {
        calls += 1;
        return list;
      },
    } as Pick<Window, "matchMedia">;

    expect(prefersReducedMotion(view)).toBe(false);
    for (let i = 0; i < 20; i += 1) prefersReducedMotion(view);
    expect(calls).toBe(1);

    // The list stays live, so a preference change lands without re-querying.
    (list as { matches: boolean }).matches = true;
    expect(prefersReducedMotion(view)).toBe(true);
    expect(calls).toBe(1);
  });

  it("re-queries when the view's matchMedia is replaced", () => {
    const first = {
      matchMedia: () => ({ matches: true }) as MediaQueryList,
    } as Pick<Window, "matchMedia">;
    const second = {
      matchMedia: () => ({ matches: false }) as MediaQueryList,
    } as Pick<Window, "matchMedia">;

    expect(prefersReducedMotion(first)).toBe(true);
    expect(prefersReducedMotion(second)).toBe(false);
    expect(prefersReducedMotion(first)).toBe(true);
  });
});
