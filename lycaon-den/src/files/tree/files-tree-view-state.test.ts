import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  flushFilesTreeViewToDisk,
  getFilesTreeScrollTop,
  resetFilesTreeViewStateForTests,
  setFilesTreeScrollTop,
  resolveFilesTreeWindowLabel,
  syncFilesTreeViewFromSnapshot,
} from "./files-tree-view-state.ts";

const nativeWindow = vi.hoisted(() => ({ active: false, label: "main" }));
vi.mock("../../platform/runtime.ts", () => ({ isTauriRuntime: () => nativeWindow.active }));
vi.mock("@tauri-apps/api/window", () => ({ getCurrentWindow: () => ({ label: nativeWindow.label }) }));

const persistAppState = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
const getAppStateSnapshot = vi.hoisted(() =>
  vi.fn(() => ({
    version: 1 as const,
    recents: [],
    filesTreeView: undefined as
      | {
          byWindow?: Record<
            string,
            {
              touchedAt: number;
              byProject: Record<string, { scrollTop?: number; touchedAt: number }>;
            }
          >;
        }
      | undefined,
  })),
);

vi.mock("../../store/app-state-snapshot.ts", () => ({
  persistAppState: (...args: unknown[]) => persistAppState(...args),
  getAppStateSnapshot: () => getAppStateSnapshot(),
}));

describe("files-tree-view-state", () => {
  beforeEach(() => {
    resetFilesTreeViewStateForTests();
    nativeWindow.active = false;
    nativeWindow.label = "main";
    persistAppState.mockClear();
    getAppStateSnapshot.mockClear();
    getAppStateSnapshot.mockReturnValue({
      version: 1,
      recents: [],
      filesTreeView: undefined,
    });
    vi.useFakeTimers();
    vi.setSystemTime(1_234);
  });

  afterEach(() => {
    vi.useRealTimers();
    resetFilesTreeViewStateForTests();
  });

  it("keeps scroll per window label when flushing", async () => {
    getAppStateSnapshot.mockReturnValue({
      version: 1,
      recents: [],
      filesTreeView: {
        byWindow: {
          other: {
            touchedAt: 100,
            byProject: { p1: { scrollTop: 99, touchedAt: 100 } },
          },
        },
      },
    });
    await resolveFilesTreeWindowLabel();
    setFilesTreeScrollTop("p1", 42);
    await flushFilesTreeViewToDisk();
    expect(persistAppState).toHaveBeenCalledWith({
      filesTreeView: {
        byWindow: {
          other: {
            touchedAt: 100,
            byProject: { p1: { scrollTop: 99, touchedAt: 100 } },
          },
          main: {
            touchedAt: 1_234,
            byProject: { p1: { scrollTop: 42, touchedAt: 1_234 } },
          },
        },
      },
    });
  });

  it("boot sync loads scroll for this window only", async () => {
    nativeWindow.active = true;
    nativeWindow.label = "peer";
    await resolveFilesTreeWindowLabel();
    getAppStateSnapshot.mockReturnValue({
      version: 1,
      recents: [],
      filesTreeView: {
        byWindow: {
          main: {
            touchedAt: 100,
            byProject: { p1: { scrollTop: 10, touchedAt: 100 } },
          },
          peer: {
            touchedAt: 100,
            byProject: { p1: { scrollTop: 55, touchedAt: 100 } },
          },
        },
      },
    });
    syncFilesTreeViewFromSnapshot();
    expect(getFilesTreeScrollTop("p1")).toBe(55);
  });
});
