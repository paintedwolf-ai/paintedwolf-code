import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { invoke, isTauriRuntime } = vi.hoisted(() => ({
  invoke: vi.fn(),
  isTauriRuntime: vi.fn(),
}));

vi.mock("@tauri-apps/api/core", () => ({ invoke }));
vi.mock("../runtime.ts", () => ({ isTauriRuntime }));

describe("setWaitingBadge", () => {
  beforeEach(() => {
    vi.resetModules();
    invoke.mockReset().mockResolvedValue(undefined);
    isTauriRuntime.mockReset().mockReturnValue(true);
    vi.spyOn(console, "debug").mockImplementation(() => undefined);
  });

  afterEach(() => vi.restoreAllMocks());

  it("does not invoke the desktop command in browser mode", async () => {
    isTauriRuntime.mockReturnValue(false);
    const { setWaitingBadge } = await import("./badge.ts");
    await setWaitingBadge(2);
    expect(invoke).not.toHaveBeenCalled();
  });

  it("normalizes counts and reuses only successfully applied values", async () => {
    const { setWaitingBadge } = await import("./badge.ts");
    await setWaitingBadge(2.9);
    await setWaitingBadge(2);
    await setWaitingBadge(-1);
    expect(invoke.mock.calls).toEqual([
      ["den_set_badge_count", { count: 2 }],
      ["den_set_badge_count", { count: 0 }],
    ]);
  });

  it("retries the same count after a failed invocation", async () => {
    invoke.mockRejectedValueOnce(new Error("permission unavailable"));
    const { setWaitingBadge } = await import("./badge.ts");
    await setWaitingBadge(3);
    await setWaitingBadge(3);
    await setWaitingBadge(3);
    expect(invoke).toHaveBeenCalledTimes(2);
    expect(invoke).toHaveBeenLastCalledWith("den_set_badge_count", { count: 3 });
  });

  it("preserves update order while an earlier invocation is pending", async () => {
    let release!: () => void;
    let started!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    const invoked = new Promise<void>((resolve) => {
      started = resolve;
    });
    invoke.mockImplementationOnce(() => {
      started();
      return pending;
    });
    const { setWaitingBadge } = await import("./badge.ts");
    const first = setWaitingBadge(2);
    await invoked;
    const second = setWaitingBadge(0);
    const third = setWaitingBadge(2);
    expect(invoke).toHaveBeenCalledOnce();
    release();
    await Promise.all([first, second, third]);
    expect(invoke.mock.calls).toEqual([
      ["den_set_badge_count", { count: 2 }],
      ["den_set_badge_count", { count: 0 }],
      ["den_set_badge_count", { count: 2 }],
    ]);
  });
});
