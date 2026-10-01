import { createRoot, createSignal } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createWindowClaim } from "./window-claim.ts";

const widenWindowBy = vi.fn(async (_deltaPx: number) => "claimed" as string);

vi.mock("../platform/windows/window-chrome.ts", () => ({
  widenWindowBy: (deltaPx: number) => widenWindowBy(deltaPx),
}));

beforeEach(() => {
  widenWindowBy.mockClear();
  widenWindowBy.mockResolvedValue("claimed");
});

describe("window claim", () => {
  it("asks for the deficit and reports nothing while the window can grow", async () => {
    await createRoot(async (dispose) => {
      const claim = createWindowClaim({
        deficitPx: () => 201,
        rearmOn: () => 0,
      });
      expect(claim.refusals()).toBe(0);
      claim.claim();
      await Promise.resolve();
      expect(widenWindowBy).toHaveBeenCalledWith(201);
      expect(claim.refusals()).toBe(0);
      dispose();
    });
  });

  it("counts each refusal, so a repeat attempt is answered again", async () => {
    widenWindowBy.mockResolvedValue("no-room");
    await createRoot(async (dispose) => {
      const claim = createWindowClaim({
        deficitPx: () => 201,
        rearmOn: () => 0,
      });
      claim.claim();
      await Promise.resolve();
      expect(claim.refusals()).toBe(1);
      claim.claim();
      await Promise.resolve();
      expect(claim.refusals()).toBe(2);
      dispose();
    });
  });

  it("re-arms on the next size change, which is how leaving fullscreen clears it", async () => {
    widenWindowBy.mockResolvedValue("no-room");
    await createRoot(async (dispose) => {
      const [viewport, setViewport] = createSignal(1000);
      const claim = createWindowClaim({
        deficitPx: () => 201,
        rearmOn: viewport,
      });
      claim.claim();
      await Promise.resolve();
      expect(claim.refusals()).toBe(1);

      setViewport(1600);
      expect(claim.refusals()).toBe(0);
      dispose();
    });
  });
});
