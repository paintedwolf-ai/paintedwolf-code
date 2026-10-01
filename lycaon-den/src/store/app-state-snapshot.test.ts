import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  flushAppState,
  loadSharedAppState,
  persistAppState,
  resetAppStateSnapshotForTests,
  setAppStateSnapshot,
} from "./app-state-snapshot.ts";
import { persistAppStateInBackground } from "./app-state-background-write.ts";

const loadAppState = vi.fn();
const patchAppState = vi.fn();

vi.mock("../platform/persistence/app-state.ts", () => ({
  loadAppState: (...args: unknown[]) => loadAppState(...args),
  patchAppState: (...args: unknown[]) => patchAppState(...args),
}));

describe("app-state-snapshot", () => {
  beforeEach(() => {
    loadAppState.mockReset();
    patchAppState.mockReset();
    patchAppState.mockImplementation(async (_patch, state) => state);
    resetAppStateSnapshotForTests();
  });

  it("discards stale disk load when snapshot was persisted during load", async () => {
    let resolveLoad!: (value: typeof EMPTY_APP_STATE_V1) => void;
    loadAppState.mockReturnValue(
      new Promise((resolve) => {
        resolveLoad = resolve;
      }),
    );

    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      display: { diffCollapsed: true },
    });

    const loadPromise = loadSharedAppState();
    await persistAppState({
      display: { diffCollapsed: false },
    });
    resolveLoad({
      ...EMPTY_APP_STATE_V1,
      display: { diffCollapsed: true },
    });

    await loadPromise;

    expect(getAppStateSnapshot().display?.diffCollapsed).toBe(false);
  });

  it("keeps pending layout preferences when Settings opens before the write starts", async () => {
    loadAppState.mockResolvedValue(EMPTY_APP_STATE_V1);
    const pending = persistAppState({ layout: { workspaceOrientation: "mirrored" } });

    const loaded = await loadSharedAppState();
    expect(loaded.layout?.workspaceOrientation).toBe("mirrored");
    expect(loadAppState).not.toHaveBeenCalled();
    await flushAppState();
    await pending;
    expect(getAppStateSnapshot().layout?.workspaceOrientation).toBe("mirrored");
  });

  it("keeps pending preferences while the disk write is in flight", async () => {
    let finishWrite!: (value: typeof EMPTY_APP_STATE_V1) => void;
    patchAppState.mockImplementationOnce(() => new Promise((resolve) => { finishWrite = resolve; }));
    loadAppState.mockResolvedValue(EMPTY_APP_STATE_V1);
    const pending = persistAppState({ layout: { workspaceOrientation: "mirrored" } });
    const flushing = flushAppState();
    await Promise.resolve();

    const loaded = await loadSharedAppState();
    expect(loaded.layout?.workspaceOrientation).toBe("mirrored");
    expect(loadAppState).not.toHaveBeenCalled();
    finishWrite(loaded);
    await Promise.all([pending, flushing]);
  });

  it("builds a patch from only the caller's keys", async () => {
    await persistAppState({ display: { diffWordWrap: true } });
    expect(patchAppState).toHaveBeenCalledTimes(1);
    const [patch] = patchAppState.mock.calls[0]!;
    expect(Object.keys(patch)).toEqual(["display"]);
    expect(patch.display).toEqual({ diffWordWrap: true });
  });

  it("a slice never named in this module still persists", async () => {
    await persistAppState({
      someFutureSlice: { x: 1 },
    } as Partial<typeof EMPTY_APP_STATE_V1>);
    const [patch] = patchAppState.mock.calls[0]!;
    expect(patch).toEqual({ someFutureSlice: { x: 1 } });
  });

  it("explicit undefined becomes a null delete", async () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      contextNav: { visible: ["files"] },
    });
    await persistAppState({ contextNav: undefined });
    const [patch] = patchAppState.mock.calls[0]!;
    expect(patch).toEqual({ contextNav: null });
    expect(getAppStateSnapshot().contextNav).toBeUndefined();
  });

  it("version is never patched", async () => {
    await persistAppState({ version: 99 } as unknown as Partial<typeof EMPTY_APP_STATE_V1>);
    expect(patchAppState).toHaveBeenCalledTimes(1);
    const [patch] = patchAppState.mock.calls[0]!;
    expect(patch).toEqual({});
    expect("version" in patch).toBe(false);
  });

  it("a stale revision does not overwrite a newer snapshot", async () => {
    let resolveSlow!: (value: typeof EMPTY_APP_STATE_V1) => void;
    patchAppState.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveSlow = resolve;
        }),
    );

    const first = persistAppState({ display: { diffCollapsed: true } });
    const flushing = flushAppState();
    await Promise.resolve();
    const second = persistAppState({ display: { diffWordWrap: true } });
    expect(getAppStateSnapshot().display?.diffWordWrap).toBe(true);

    resolveSlow({
      ...EMPTY_APP_STATE_V1,
      display: { diffCollapsed: true },
    });
    await Promise.all([first, second, flushing]);

    expect(getAppStateSnapshot().display?.diffWordWrap).toBe(true);
  });

  it("merges same-turn patches into one write", async () => {
    const first = persistAppState({ display: { diffCollapsed: true } });
    const second = persistAppState({ contextNav: { visible: ["files"] } });
    const third = persistAppState({ display: { diffWordWrap: true } });
    expect(patchAppState).toHaveBeenCalledTimes(0);
    const flushing = flushAppState();
    await Promise.all([first, second, third, flushing]);
    expect(patchAppState).toHaveBeenCalledTimes(1);
    const [patch] = patchAppState.mock.calls[0]!;
    expect(Object.keys(patch).sort()).toEqual(["contextNav", "display"]);
    expect(patch.display).toEqual({ diffWordWrap: true });
  });

  it("merges patches issued while a write is in flight", async () => {
    let resolveSlow!: (value: typeof EMPTY_APP_STATE_V1) => void;
    patchAppState.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveSlow = resolve;
        }),
    );

    const first = persistAppState({ display: { diffCollapsed: true } });
    const flushing = flushAppState();
    await Promise.resolve();
    expect(patchAppState).toHaveBeenCalledTimes(1);

    const second = persistAppState({ contextNav: { visible: ["files"] } });
    const third = persistAppState({ display: { diffWordWrap: true } });
    expect(patchAppState).toHaveBeenCalledTimes(1);

    resolveSlow({ ...EMPTY_APP_STATE_V1 });
    await Promise.all([first, second, third, flushing]);

    expect(patchAppState).toHaveBeenCalledTimes(2);
    const [mergedPatch] = patchAppState.mock.calls[1]!;
    expect(Object.keys(mergedPatch).sort()).toEqual(["contextNav", "display"]);
    expect(mergedPatch.display).toEqual({ diffWordWrap: true });
  });

  it("resolves each caller after its patch reaches disk", async () => {
    let resolveFirst!: (value: typeof EMPTY_APP_STATE_V1) => void;
    let resolveSecond!: (value: typeof EMPTY_APP_STATE_V1) => void;
    patchAppState
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveFirst = resolve;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveSecond = resolve;
          }),
      );

    const first = persistAppState({ display: { diffCollapsed: true } });
    const flushing = flushAppState();
    await Promise.resolve();
    const second = persistAppState({ contextNav: { visible: ["files"] } });
    let secondSettled = false;
    void second.then(() => {
      secondSettled = true;
    });

    resolveFirst({ ...EMPTY_APP_STATE_V1 });
    await first;
    expect(secondSettled).toBe(false);
    expect(patchAppState).toHaveBeenCalledTimes(2);

    resolveSecond({ ...EMPTY_APP_STATE_V1 });
    await Promise.all([second, flushing]);
  });

  it("optimistic apply is visible before the write settles", async () => {
    let resolveWrite!: (value: typeof EMPTY_APP_STATE_V1) => void;
    patchAppState.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveWrite = resolve;
        }),
    );

    const pending = persistAppState({ display: { diffCollapsed: true } });
    expect(getAppStateSnapshot().display?.diffCollapsed).toBe(true);
    const flushing = flushAppState();
    await Promise.resolve();

    resolveWrite({
      ...EMPTY_APP_STATE_V1,
      display: { diffCollapsed: true },
    });
    await Promise.all([pending, flushing]);
  });

  it("merges a sustained burst into one forced durability write", async () => {
    const pending = Array.from({ length: 50 }, (_, index) =>
      persistAppState({ display: { editorZoom: index } } as Partial<typeof EMPTY_APP_STATE_V1>),
    );

    await Promise.all([...pending, flushAppState()]);

    expect(patchAppState).toHaveBeenCalledTimes(1);
    expect(patchAppState.mock.calls[0]?.[0]).toEqual({
      display: { editorZoom: 49 },
    });
  });

  it("retains a failed patch for the next durability attempt", async () => {
    patchAppState.mockRejectedValueOnce(new Error("disk busy"));
    const first = persistAppState({ display: { diffCollapsed: true } });
    await expect(Promise.all([first, flushAppState()])).rejects.toThrow("disk busy");

    const second = persistAppState({ contextNav: { visible: ["files"] } });
    await Promise.all([second, flushAppState()]);

    expect(patchAppState).toHaveBeenCalledTimes(2);
    expect(patchAppState.mock.calls[1]?.[0]).toEqual({
      display: { diffCollapsed: true },
      contextNav: { visible: ["files"] },
    });
  });

  it("a background write resolves on failure and its patch rides the next write", async () => {
    patchAppState.mockRejectedValueOnce(new Error("disk busy"));
    const first = persistAppStateInBackground({ display: { diffCollapsed: true } });
    await Promise.all([first, flushAppState().catch(() => undefined)]);
    await expect(first).resolves.toBeUndefined();

    const second = persistAppStateInBackground({ contextNav: { visible: ["files"] } });
    await Promise.all([second, flushAppState()]);

    expect(patchAppState).toHaveBeenCalledTimes(2);
    expect(patchAppState.mock.calls[1]?.[0]).toEqual({
      display: { diffCollapsed: true },
      contextNav: { visible: ["files"] },
    });
  });
});

afterEach(() => {
  vi.useRealTimers();
});
