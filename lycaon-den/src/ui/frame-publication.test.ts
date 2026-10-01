import { describe, expect, it, vi } from "vitest";
import { createFramePublication } from "./frame-publication.ts";

type AnimationFrameScheduler = NonNullable<
  Parameters<typeof createFramePublication>[1]
>;

describe("frame publication", () => {
  it("publishes only the latest value scheduled in a frame", () => {
    let callback: FrameRequestCallback | undefined;
    const scheduler: AnimationFrameScheduler = {
      request: vi.fn((next) => {
        callback = next;
        return 7;
      }),
      cancel: vi.fn(),
    };
    const publish = vi.fn();
    const publication = createFramePublication(publish, scheduler);

    publication.schedule("first");
    publication.schedule("latest");
    expect(scheduler.request).toHaveBeenCalledTimes(1);
    callback?.(16);

    expect(publish).toHaveBeenCalledTimes(1);
    expect(publish).toHaveBeenCalledWith("latest");
  });

  it("cancels an unpublished frame", () => {
    const scheduler: AnimationFrameScheduler = {
      request: vi.fn(() => 11),
      cancel: vi.fn(),
    };
    const publish = vi.fn();
    const publication = createFramePublication(publish, scheduler);
    publication.schedule("pending");
    publication.cancel();

    expect(scheduler.cancel).toHaveBeenCalledWith(11);
    expect(publish).not.toHaveBeenCalled();
  });
});
