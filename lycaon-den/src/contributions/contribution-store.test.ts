import { stubClient } from "../test/client-fixture.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createEffect, createRoot } from "solid-js";
import type { LycaonClient } from "../api/client.ts";
import {
  contributionFrame,
  contributionFrameReady,
  contributionFrameState,
  initContributionStore,
  ensureContributionFrame,
  invalidateContributionFrame,
  onContributionFrameChange,
  resetContributionStoreForTest,
  replayContributionFrameMemoForTest,
  setContributionRetrySleepForTest,
} from "./contribution-store.ts";
import type { ContributionFrameResponse } from "../api/types.ts";
import { writeContributionFrameCache } from "./contribution-frame-cache.ts";
import { STOCK_FRAME } from "./stock-frame.generated.ts";

function emptyFrame(revision: string): ContributionFrameResponse {
  return {
    frame_revision: revision,
    commands: [],
    menus: [],
    keybindings: [],
    binding_defaults: [],
    editor_actions: [],
    configuration: [],
    requirements: [],
    search_sources: [],
    operations: [],
    themes: [],
    notes: [],
  };
}

function clientWith(
  list: () => Promise<ContributionFrameResponse>,
): LycaonClient {
  return stubClient({ getContributions: vi.fn(list) });
}

afterEach(() => {
  resetContributionStoreForTest();
});

describe("contribution store", () => {
  it("rejects incomplete theme paint before notifying consumers and recovers with a complete frame", async () => {
    resetContributionStoreForTest();
    setContributionRetrySleepForTest(() => Promise.resolve());
    const malformed = structuredClone(STOCK_FRAME);
    for (const theme of malformed.themes) Reflect.deleteProperty(theme, "window_colors");
    const list = vi.fn().mockResolvedValueOnce(malformed).mockResolvedValue(STOCK_FRAME);
    const changed = vi.fn();
    const stop = onContributionFrameChange(changed);
    const client = clientWith(list);
    initContributionStore(() => client);
    await vi.waitFor(() => expect(contributionFrame()).toEqual(STOCK_FRAME));
    expect(list).toHaveBeenCalledTimes(2);
    expect(changed).toHaveBeenCalledExactlyOnceWith({ phase: "ready", frame: STOCK_FRAME });
    stop();
  });

  it("starts in the defined empty state and swaps one complete frame", async () => {
    resetContributionStoreForTest();
    expect(contributionFrame()).toBeNull();
    const client = clientWith(() => Promise.resolve(emptyFrame("r1")));
    initContributionStore(() => client);
    await vi.waitFor(() => expect(contributionFrame()?.frame_revision).toBe("r1"));
    expect(contributionFrameReady()).toBe(true);
  });

  it("keeps the previous complete frame across a failed hydrate", async () => {
    let fail = false;
    const client = clientWith(() =>
      fail ? Promise.reject(new Error("down")) : Promise.resolve(emptyFrame("r1")),
    );
    initContributionStore(() => client);
    await vi.waitFor(() => expect(contributionFrame()?.frame_revision).toBe("r1"));

    fail = true;
    void invalidateContributionFrame();
    expect(contributionFrame()?.frame_revision).toBe("r1");
    await invalidateContributionFrame();
    expect(contributionFrame()?.frame_revision).toBe("r1");
  });

  it("coalesces invalidations into one in-flight hydrate plus one follow-up", async () => {
    let calls = 0;
    let release: (() => void) | undefined;
    const client = clientWith(() => {
      calls += 1;
      return new Promise((resolve) => {
        release = () => resolve(emptyFrame(`r${calls}`));
      });
    });
    initContributionStore(() => client);
    void invalidateContributionFrame();
    void invalidateContributionFrame();
    void invalidateContributionFrame();
    expect(calls).toBe(1);
    release?.();
    await vi.waitFor(() => expect(calls).toBe(2));
    release?.();
    await vi.waitFor(() => expect(contributionFrame()?.frame_revision).toBe("r2"));
  });

  it("notifies reactive consumers when the frame becomes ready", async () => {
    resetContributionStoreForTest();
    expect(contributionFrameReady()).toBe(false);
    const client = clientWith(() => Promise.resolve(emptyFrame("r1")));
    const ready: boolean[] = [];
    const dispose = createRoot((release) => {
      createEffect(() => ready.push(contributionFrameReady()));
      return release;
    });
    expect(ready).toEqual([false]);
    initContributionStore(() => client);

    await vi.waitFor(() => expect(ready[ready.length - 1]).toBe(true));
    expect(ready).toContain(false);
    dispose();
  });

  it("does not retrigger ready when the same frame refreshes", async () => {
    let calls = 0;
    const client = clientWith(() => {
      calls += 1;
      return Promise.resolve(emptyFrame("r-same"));
    });
    initContributionStore(() => client);
    await vi.waitFor(() => expect(contributionFrame()?.frame_revision).toBe("r-same"));

    const ready: boolean[] = [];
    const dispose = createRoot((release) => {
      createEffect(() => ready.push(contributionFrameReady()));
      return release;
    });
    expect(ready).toEqual([true]);

    void invalidateContributionFrame();
    await vi.waitFor(() => expect(calls).toBe(2));
    expect(ready).toEqual([true]);
    dispose();
  });

  it("hydrates the device frame at startup", async () => {
    resetContributionStoreForTest();
    const client = clientWith(() => Promise.resolve(emptyFrame("rev-device")));
    initContributionStore(() => client);
    await vi.waitFor(() =>
      expect(contributionFrame()?.frame_revision).toBe("rev-device"),
    );
    expect(client.getContributions).toHaveBeenCalledWith();
  });

  it("leaves the hydrate lane free until backend discovery installs a client", async () => {
    resetContributionStoreForTest();
    let client: LycaonClient | null = null;
    const discovered = clientWith(() => Promise.resolve(emptyFrame("rev-device")));
    initContributionStore(() => client);

    expect(contributionFrame()).toBeNull();
    client = discovered;
    await invalidateContributionFrame();

    expect(contributionFrame()?.frame_revision).toBe("rev-device");
    expect(discovered.getContributions).toHaveBeenCalledWith();
  });

  it("retries a failing hydrate, then degrades loudly with a notice", async () => {
    resetContributionStoreForTest();
    setContributionRetrySleepForTest(() => Promise.resolve());
    let attempts = 0;
    const client = clientWith(() => {
      attempts += 1;
      return Promise.reject(new Error("503"));
    });
    const reportError = vi.fn();
    initContributionStore(() => client, () => ({ reportError, publish: vi.fn() }));
    await vi.waitFor(() => expect(reportError).toHaveBeenCalledTimes(1));
    expect(attempts).toBe(5);
    expect(reportError.mock.calls[0]?.[0]).toMatchObject({
      kind: "contribution_frame_unavailable",
    });
    expect(contributionFrame()).toBeNull();
    expect(contributionFrameState()).toEqual({
      phase: "unavailable",
      frame: null,
    });
  });

  it("recovers on a later attempt without ever degrading", async () => {
    resetContributionStoreForTest();
    setContributionRetrySleepForTest(() => Promise.resolve());
    let attempts = 0;
    const client = clientWith(() => {
      attempts += 1;
      return attempts < 3
        ? Promise.reject(new Error("not ready"))
        : Promise.resolve(emptyFrame("r-late"));
    });
    const reportError = vi.fn();
    initContributionStore(() => client, () => ({ reportError, publish: vi.fn() }));
    await vi.waitFor(() => expect(contributionFrame()?.frame_revision).toBe("r-late"));
    expect(reportError).not.toHaveBeenCalled();
  });

  it("says nothing about a failed refresh while a frame still renders", async () => {
    resetContributionStoreForTest();
    setContributionRetrySleepForTest(() => Promise.resolve());
    let fail = false;
    const client = clientWith(() =>
      fail ? Promise.reject(new Error("down")) : Promise.resolve(emptyFrame("r1")),
    );
    const reportError = vi.fn();
    initContributionStore(() => client, () => ({ reportError, publish: vi.fn() }));
    await vi.waitFor(() => expect(contributionFrame()?.frame_revision).toBe("r1"));

    fail = true;
    await ensureContributionFrame();
    expect(contributionFrame()?.frame_revision).toBe("r1");
    expect(reportError).not.toHaveBeenCalled();
  });

  it("rehydrates the device frame on invalidation", async () => {
    resetContributionStoreForTest();
    let calls = 0;
    const client = clientWith(() => {
      calls += 1;
      return Promise.resolve(emptyFrame(`r${calls}`));
    });
    initContributionStore(() => client);
    await vi.waitFor(() => expect(contributionFrame()?.frame_revision).toBe("r1"));
    void invalidateContributionFrame();
    await vi.waitFor(() => expect(contributionFrame()?.frame_revision).toBe("r2"));
  });

  it("replays a memoized frame before getContributions returns", async () => {
    resetContributionStoreForTest();
    writeContributionFrameCache(emptyFrame("memo-r"));
    replayContributionFrameMemoForTest();
    expect(contributionFrame()?.frame_revision).toBe("memo-r");
    expect(contributionFrameReady()).toBe(true);

    let release: (() => void) | undefined;
    const client = clientWith(
      () =>
        new Promise((resolve) => {
          release = () => resolve(emptyFrame("live-r"));
        }),
    );
    initContributionStore(() => client);
    expect(contributionFrame()?.frame_revision).toBe("memo-r");
    release?.();
    await vi.waitFor(() =>
      expect(contributionFrame()?.frame_revision).toBe("live-r"),
    );
  });

  it("notifies listeners when a hydrate commits", async () => {
    resetContributionStoreForTest();
    const client = clientWith(() => Promise.resolve(emptyFrame("r1")));
    const changed = vi.fn();
    initContributionStore(() => client);
    const stop = onContributionFrameChange(changed);
    await vi.waitFor(() => expect(contributionFrame()?.frame_revision).toBe("r1"));
    expect(changed).toHaveBeenCalledWith({
      phase: "ready",
      frame: emptyFrame("r1"),
    });
    stop();
  });
});
