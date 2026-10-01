// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const runtime = vi.hoisted(() => ({ tauri: false }));
const thisLabel = "main";
const listenMock = vi.hoisted(() =>
  vi.fn(
    async (
      _event: string,
      handler: (event: { payload: unknown }) => void,
    ) => {
      listenHandler = handler;
      return unlistenMock;
    },
  ),
);
const unlistenMock = vi.hoisted(() => vi.fn());
let listenHandler: ((event: { payload: unknown }) => void) | null = null;

const syncLayoutFromSnapshot = vi.hoisted(() => vi.fn());
const syncDebugPrefsFromSnapshot = vi.hoisted(() => vi.fn());
const syncDisplayPrefsFromSnapshot = vi.hoisted(() => vi.fn());
const syncNotificationPrefsFromSnapshot = vi.hoisted(() => vi.fn());
const syncEditorPrefsFromSnapshot = vi.hoisted(() => vi.fn());
const syncContextNavPrefsFromSnapshot = vi.hoisted(() => vi.fn());
const syncOnboardingPrefsFromSnapshot = vi.hoisted(() => vi.fn());
const syncWhatsNewFromSnapshot = vi.hoisted(() => vi.fn());
const syncShortcutPrefsFromSnapshot = vi.hoisted(() => vi.fn());
const syncExternalOpenPrefsFromSnapshot = vi.hoisted(() => vi.fn());
const syncFirstTimeTipsFromSnapshot = vi.hoisted(() => vi.fn());
const loadTranscriptRowHeightsFromSnapshot = vi.hoisted(() => vi.fn());
const syncComposerDraftsFromSnapshot = vi.hoisted(() => vi.fn());
const syncFilesHotExitFromSnapshot = vi.hoisted(() => vi.fn());
const syncSessionFidelityFromSnapshot = vi.hoisted(() => vi.fn());
const syncFilesTreeViewFromSnapshot = vi.hoisted(() => vi.fn());
const syncReviewPaneFromSnapshot = vi.hoisted(() => vi.fn());
const setAppStateSnapshot = vi.hoisted(() => vi.fn());
const parseAppState = vi.hoisted(() =>
  vi.fn((raw: unknown) => ({
    version: 1,
    ...(raw && typeof raw === "object" && !Array.isArray(raw) ? raw : {}),
  })),
);

vi.mock("../runtime.ts", () => ({
  isTauriRuntime: () => runtime.tauri,
}));

vi.mock("@tauri-apps/api/window", () => ({
  getCurrentWindow: () => ({ label: thisLabel }),
}));

vi.mock("@tauri-apps/api/event", () => ({
  listen: (...args: unknown[]) =>
    listenMock(...(args as [string, (event: { payload: unknown }) => void])),
}));

vi.mock("../../shell/layout-store.ts", () => ({
  syncLayoutFromSnapshot: (...args: unknown[]) =>
    syncLayoutFromSnapshot(...args),
}));
vi.mock("../../settings/system/debug-prefs.ts", () => ({
  syncDebugPrefsFromSnapshot: (...args: unknown[]) =>
    syncDebugPrefsFromSnapshot(...args),
}));
vi.mock("../../settings/appearance/display-prefs.ts", () => ({
  syncDisplayPrefsFromSnapshot: (...args: unknown[]) =>
    syncDisplayPrefsFromSnapshot(...args),
}));
vi.mock("../../settings/chat/notification-prefs.ts", () => ({
  syncNotificationPrefsFromSnapshot: (...args: unknown[]) =>
    syncNotificationPrefsFromSnapshot(...args),
}));
vi.mock("../../settings/editor/editor-prefs.ts", () => ({
  syncEditorPrefsFromSnapshot: (...args: unknown[]) =>
    syncEditorPrefsFromSnapshot(...args),
}));
vi.mock("../../settings/editor/context-nav-prefs.ts", () => ({
  syncContextNavPrefsFromSnapshot: (...args: unknown[]) =>
    syncContextNavPrefsFromSnapshot(...args),
}));
vi.mock("../../settings/system/onboarding-prefs.ts", () => ({
  syncOnboardingPrefsFromSnapshot: (...args: unknown[]) =>
    syncOnboardingPrefsFromSnapshot(...args),
}));
vi.mock("../../settings/system/whats-new-prefs.ts", () => ({
  syncWhatsNewFromSnapshot: (...args: unknown[]) =>
    syncWhatsNewFromSnapshot(...args),
}));
vi.mock("../../settings/system/shortcut-prefs.ts", () => ({
  syncShortcutPrefsFromSnapshot: (...args: unknown[]) =>
    syncShortcutPrefsFromSnapshot(...args),
}));
vi.mock("../../settings/editor/external-open-prefs.ts", () => ({
  syncExternalOpenPrefsFromSnapshot: (...args: unknown[]) =>
    syncExternalOpenPrefsFromSnapshot(...args),
}));
vi.mock("../../settings/system/first-time-tips-prefs.ts", () => ({
  syncFirstTimeTipsFromSnapshot: (...args: unknown[]) =>
    syncFirstTimeTipsFromSnapshot(...args),
}));
vi.mock("../../chat/transcript/layout/transcript-row-heights-persist.ts", () => ({
  loadTranscriptRowHeightsFromSnapshot: (...args: unknown[]) =>
    loadTranscriptRowHeightsFromSnapshot(...args),
}));
vi.mock("../../chat/composer/composer-drafts.ts", () => ({
  syncComposerDraftsFromSnapshot: (...args: unknown[]) =>
    syncComposerDraftsFromSnapshot(...args),
}));
vi.mock("../../files/documents/files-hot-exit.ts", () => ({
  syncFilesHotExitFromSnapshot: (...args: unknown[]) =>
    syncFilesHotExitFromSnapshot(...args),
}));
vi.mock("../../files/editor/editor-session-fidelity.ts", () => ({
  syncSessionFidelityFromSnapshot: (...args: unknown[]) =>
    syncSessionFidelityFromSnapshot(...args),
}));
vi.mock("../../files/tree/files-tree-view-state.ts", () => ({
  syncFilesTreeViewFromSnapshot: (...args: unknown[]) =>
    syncFilesTreeViewFromSnapshot(...args),
}));
vi.mock("../../files/review/review-pane.ts", () => ({
  syncReviewPaneFromSnapshot: (...args: unknown[]) =>
    syncReviewPaneFromSnapshot(...args),
}));
vi.mock("../../store/app-state-snapshot.ts", () => ({
  setAppStateSnapshot: (...args: unknown[]) => setAppStateSnapshot(...args),
  getAppStateSnapshot: () => ({ version: 1 }),
}));

vi.mock("./app-state-parse.ts", () => ({
  parseAppState: (raw: unknown) => parseAppState(raw),
}));

import {
  applyBootAppStateSnapshot,
  applyDevicePrefsFromSnapshot,
  subscribeAppStateBroadcast,
} from "./app-state-sync.ts";

function clearSyncMocks(): void {
  syncLayoutFromSnapshot.mockClear();
  syncDebugPrefsFromSnapshot.mockClear();
  syncDisplayPrefsFromSnapshot.mockClear();
  syncNotificationPrefsFromSnapshot.mockClear();
  syncEditorPrefsFromSnapshot.mockClear();
  syncContextNavPrefsFromSnapshot.mockClear();
  syncOnboardingPrefsFromSnapshot.mockClear();
  syncWhatsNewFromSnapshot.mockClear();
  syncShortcutPrefsFromSnapshot.mockClear();
  syncExternalOpenPrefsFromSnapshot.mockClear();
  syncFirstTimeTipsFromSnapshot.mockClear();
  loadTranscriptRowHeightsFromSnapshot.mockClear();
  syncComposerDraftsFromSnapshot.mockClear();
  syncFilesHotExitFromSnapshot.mockClear();
  syncSessionFidelityFromSnapshot.mockClear();
  syncFilesTreeViewFromSnapshot.mockClear();
  syncReviewPaneFromSnapshot.mockClear();
  setAppStateSnapshot.mockClear();
  parseAppState.mockClear();
  listenMock.mockClear();
  unlistenMock.mockClear();
  listenHandler = null;
}

describe("app-state-sync", () => {
  beforeEach(() => {
    runtime.tauri = false;
    clearSyncMocks();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("boot applies all four groups", () => {
    applyBootAppStateSnapshot();
    expect(syncLayoutFromSnapshot).toHaveBeenCalledOnce();
    expect(syncExternalOpenPrefsFromSnapshot).toHaveBeenCalledOnce();
    expect(syncFirstTimeTipsFromSnapshot).toHaveBeenCalledOnce();
    expect(loadTranscriptRowHeightsFromSnapshot).toHaveBeenCalledOnce();
    expect(syncComposerDraftsFromSnapshot).toHaveBeenCalledOnce();
    expect(syncFilesHotExitFromSnapshot).toHaveBeenCalledOnce();
    expect(syncSessionFidelityFromSnapshot).toHaveBeenCalledOnce();
    expect(syncFilesTreeViewFromSnapshot).toHaveBeenCalledOnce();
    expect(syncReviewPaneFromSnapshot).toHaveBeenCalledOnce();
  });

  it("device prefs omit the surface-specific destructive syncs", () => {
    applyDevicePrefsFromSnapshot();
    expect(syncLayoutFromSnapshot).toHaveBeenCalledOnce();
    expect(loadTranscriptRowHeightsFromSnapshot).not.toHaveBeenCalled();
    expect(syncComposerDraftsFromSnapshot).not.toHaveBeenCalled();
    expect(syncFilesHotExitFromSnapshot).not.toHaveBeenCalled();
    expect(syncSessionFidelityFromSnapshot).not.toHaveBeenCalled();
    expect(syncFilesTreeViewFromSnapshot).not.toHaveBeenCalled();
    expect(syncReviewPaneFromSnapshot).not.toHaveBeenCalled();
  });

  it("no-op outside Tauri", () => {
    runtime.tauri = false;
    const stop = subscribeAppStateBroadcast();
    expect(typeof stop).toBe("function");
    expect(listenMock).not.toHaveBeenCalled();
    stop();
  });

  it("ignores a broadcast this window originated", async () => {
    runtime.tauri = true;
    const stop = subscribeAppStateBroadcast();
    await vi.waitFor(() => expect(listenMock).toHaveBeenCalledOnce());
    expect(listenHandler).not.toBeNull();
    listenHandler!({
      payload: {
        originLabel: thisLabel,
        patch: { display: { diffWordWrap: true } },
      },
    });
    expect(setAppStateSnapshot).not.toHaveBeenCalled();
    expect(syncLayoutFromSnapshot).not.toHaveBeenCalled();
    expect(loadTranscriptRowHeightsFromSnapshot).not.toHaveBeenCalled();
    stop();
  });

  it("applies a current host broadcast from another window", async () => {
    runtime.tauri = true;
    const stop = subscribeAppStateBroadcast();
    await vi.waitFor(() => expect(listenMock).toHaveBeenCalledOnce());
    listenHandler!({
      payload: {
        originLabel: "stage-files-1",
        // Peers receive only the changed slices.
        patch: { display: { diffWordWrap: true } },
      },
    });
    expect(setAppStateSnapshot).toHaveBeenCalledOnce();
    expect(setAppStateSnapshot.mock.calls[0]![0]).toMatchObject({
      display: { diffWordWrap: true },
    });
    // Unchanged slices remain in the local snapshot.
    expect(parseAppState).toHaveBeenCalledWith(
      expect.objectContaining({ display: { diffWordWrap: true } }),
    );
    expect(syncLayoutFromSnapshot).toHaveBeenCalledOnce();
    expect(syncDisplayPrefsFromSnapshot).toHaveBeenCalledOnce();
    stop();
  });

  it("a broadcast never runs the destructive syncs", async () => {
    runtime.tauri = true;
    const stop = subscribeAppStateBroadcast();
    await vi.waitFor(() => expect(listenMock).toHaveBeenCalledOnce());
    listenHandler!({
      payload: {
        originLabel: "other",
        patch: { display: { diffWordWrap: true } },
      },
    });
    expect(loadTranscriptRowHeightsFromSnapshot).not.toHaveBeenCalled();
    expect(syncComposerDraftsFromSnapshot).not.toHaveBeenCalled();
    expect(syncFilesHotExitFromSnapshot).not.toHaveBeenCalled();
    expect(syncSessionFidelityFromSnapshot).not.toHaveBeenCalled();
    expect(syncFilesTreeViewFromSnapshot).not.toHaveBeenCalled();
    expect(syncReviewPaneFromSnapshot).not.toHaveBeenCalled();
    stop();
  });

  it("ignores a payload with no patch", async () => {
    runtime.tauri = true;
    const stop = subscribeAppStateBroadcast();
    await vi.waitFor(() => expect(listenMock).toHaveBeenCalledOnce());
    listenHandler!({
      payload: { originLabel: "other" },
    });
    expect(setAppStateSnapshot).not.toHaveBeenCalled();
    expect(syncLayoutFromSnapshot).not.toHaveBeenCalled();
    stop();
  });
});
