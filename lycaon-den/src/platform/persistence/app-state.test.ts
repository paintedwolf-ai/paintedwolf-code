// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import { appStateStorageSliceKey } from "../../../shared/app-state-storage.ts";

const runtime = vi.hoisted(() => ({ tauri: false }));
const invokeMock = vi.hoisted(() => vi.fn());

vi.mock("../runtime.ts", () => ({
  isTauriRuntime: () => runtime.tauri,
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invokeMock(...args),
}));

import { patchAppState } from "./app-state.ts";

describe("patchAppState", () => {
  beforeEach(() => {
    localStorage.clear();
    runtime.tauri = false;
    invokeMock.mockReset();
  });

  afterEach(() => {
    localStorage.clear();
    vi.useRealTimers();
  });

  it("localStorage merge matches the host — replace, null-delete, version skip", async () => {
    localStorage.setItem(appStateStorageSliceKey("recents"), JSON.stringify([]));
    localStorage.setItem(
      appStateStorageSliceKey("display"),
      JSON.stringify({ diffWordWrap: true }),
    );
    localStorage.setItem(
      appStateStorageSliceKey("layout"),
      JSON.stringify({ navCollapsed: false }),
    );

    const afterDisplay = await patchAppState(
      { display: { diffCollapsed: true } },
      { ...EMPTY_APP_STATE_V1 },
    );
    expect(afterDisplay.display).toEqual({ diffCollapsed: true });
    expect(afterDisplay.layout).toEqual({ navCollapsed: false });
    expect(afterDisplay.version).toBe(1);

    const afterNull = await patchAppState({ display: null }, afterDisplay);
    expect(afterNull.display).toBeUndefined();
    expect(afterNull.layout).toEqual({ navCollapsed: false });

    const withVersionAttempt = await patchAppState(
      { version: 99 } as never,
      afterNull,
    );
    expect(withVersionAttempt.version).toBe(1);

    expect(localStorage.getItem(appStateStorageSliceKey("display"))).toBeNull();
    expect(JSON.parse(localStorage.getItem(appStateStorageSliceKey("layout"))!)).toEqual({
      navCollapsed: false,
    });
  });

  it("ignores a corrupt slice without hiding independent state", async () => {
    localStorage.setItem(appStateStorageSliceKey("display"), "{broken");
    localStorage.setItem(
      appStateStorageSliceKey("layout"),
      JSON.stringify({ navCollapsed: true }),
    );

    const result = await patchAppState(
      { recents: [] },
      { ...EMPTY_APP_STATE_V1 },
    );

    expect(result.layout).toEqual({ navCollapsed: true });
    expect(result.display).toBeUndefined();
  });

  it("rejects a hung host patch so the durability queue can retry it", async () => {
    vi.useFakeTimers();
    runtime.tauri = true;
    invokeMock.mockReturnValue(new Promise(() => {}));
    const fallback = {
      ...EMPTY_APP_STATE_V1,
      display: { diffWordWrap: true },
    };
    const pending = patchAppState({ layout: { navCollapsed: true } }, fallback);
    const rejected = expect(pending).rejects.toThrow("app state write timed out");
    await vi.runAllTimersAsync();
    await rejected;
  });

  it("propagates a rejected host patch", async () => {
    runtime.tauri = true;
    invokeMock.mockRejectedValue(new Error("disk busy"));

    await expect(
      patchAppState({ layout: { navCollapsed: true } }, EMPTY_APP_STATE_V1),
    ).rejects.toThrow("disk busy");
  });
});
