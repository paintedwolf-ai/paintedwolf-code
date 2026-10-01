/** Snapshot projections for boot and device-wide updates. */
import { mergePeerTreeIntents } from "../../files/tree/files-tree-intent-state.ts";
import {
  applyContributionThemeFrame,
  startAppearanceWatch,
  syncAppearanceFromSnapshot,
} from "../../settings/appearance/appearance-prefs.ts";
import { onContributionFrameChange } from "../../contributions/contribution-store.ts";
import { syncComposerDraftsFromSnapshot } from "../../chat/composer/composer-drafts.ts";
import { loadTranscriptRowHeightsFromSnapshot } from "../../chat/transcript/layout/transcript-row-heights-persist.ts";
import { syncFilesHotExitFromSnapshot } from "../../files/documents/files-hot-exit.ts";
import { syncFilesTreeViewFromSnapshot } from "../../files/tree/files-tree-view-state.ts";
import { syncReviewPaneFromSnapshot } from "../../files/review/review-pane.ts";
import { syncSessionFidelityFromSnapshot } from "../../files/editor/editor-session-fidelity.ts";
import { syncContextNavPrefsFromSnapshot } from "../../settings/editor/context-nav-prefs.ts";
import { syncChatListPrefsFromSnapshot } from "../../settings/chat/chat-list-prefs.ts";
import { syncDebugPrefsFromSnapshot } from "../../settings/system/debug-prefs.ts";
import { syncDisplayPrefsFromSnapshot } from "../../settings/appearance/display-prefs.ts";
import { syncChatPrefsFromSnapshot } from "../../settings/chat/chat-prefs.ts";
import { syncEditorPrefsFromSnapshot } from "../../settings/editor/editor-prefs.ts";
import { syncExternalOpenPrefsFromSnapshot } from "../../settings/editor/external-open-prefs.ts";
import { syncFontPrefsFromSnapshot } from "../../settings/appearance/font-prefs.ts";
import { syncProductTextScaleFromSnapshot } from "../../settings/appearance/text-scale-prefs.ts";
import { syncFirstTimeTipsFromSnapshot } from "../../settings/system/first-time-tips-prefs.ts";
import { syncNotificationPrefsFromSnapshot } from "../../settings/chat/notification-prefs.ts";
import { syncOnboardingPrefsFromSnapshot } from "../../settings/system/onboarding-prefs.ts";
import { syncShortcutPrefsFromSnapshot } from "../../settings/system/shortcut-prefs.ts";
import { syncWhatsNewFromSnapshot } from "../../settings/system/whats-new-prefs.ts";
import { syncLayoutFromSnapshot } from "../../shell/layout-store.ts";
import { parseAppState } from "./app-state-parse.ts";
import { isTauriRuntime } from "../runtime.ts";
import {
  getAppStateSnapshot,
  setAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { listenHostEvent } from "../windows/window-channel.ts";

let stopFramePreferenceSync: (() => void) | null = null;
let stopAppearancePreferenceSync: (() => void) | null = null;

/** Apply device-wide preferences from the current snapshot. */
export function applyDevicePrefsFromSnapshot(): void {
  syncLayoutFromSnapshot();
  syncDebugPrefsFromSnapshot();
  syncDisplayPrefsFromSnapshot();
  syncChatPrefsFromSnapshot();
  syncNotificationPrefsFromSnapshot();
  syncEditorPrefsFromSnapshot();
  syncContextNavPrefsFromSnapshot();
  syncChatListPrefsFromSnapshot();
  syncOnboardingPrefsFromSnapshot();
  syncWhatsNewFromSnapshot();
  syncFirstTimeTipsFromSnapshot();
  syncAppearanceFromSnapshot();
  syncFontPrefsFromSnapshot();
  syncProductTextScaleFromSnapshot();
  syncShortcutPrefsFromSnapshot();
  syncExternalOpenPrefsFromSnapshot();
}

/** Apply the complete boot snapshot. */
export function applyBootAppStateSnapshot(): void {
  applyDevicePrefsFromSnapshot();
  loadTranscriptRowHeightsFromSnapshot();
  syncComposerDraftsFromSnapshot();
  syncFilesHotExitFromSnapshot();
  syncSessionFidelityFromSnapshot();
  syncFilesTreeViewFromSnapshot();
  syncReviewPaneFromSnapshot();
  stopFramePreferenceSync?.();
  stopFramePreferenceSync = onContributionFrameChange((state) => {
    applyContributionThemeFrame(state);
    syncShortcutPrefsFromSnapshot();
  });
  // The stored mode resolves the scheme independently of project state.
  stopAppearancePreferenceSync?.();
  stopAppearancePreferenceSync = startAppearanceWatch();
}

/** Reconcile device preferences from peer-window writes. */
export function subscribeAppStateBroadcast(): () => void {
  if (!isTauriRuntime()) {
    return () => {};
  }

  let disposed = false;
  let unlisten: (() => void) | null = null;

  void (async () => {
    try {
      const { getCurrentWindow } = await import("@tauri-apps/api/window");
      const thisLabel = getCurrentWindow().label;
      const stop = await listenHostEvent<{
        originLabel?: string;
        patch?: Record<string, unknown>;
      }>("app-state-changed", (event) => {
        if (disposed) return;
        const payload = event.payload;
        // Ignore the origin window's already-applied echo.
        if (payload?.originLabel === thisLabel) return;
        const patch = payload?.patch;
        if (!patch) return;
        // Merge only the slices changed by the peer window.
        const next: Record<string, unknown> = { ...getAppStateSnapshot() };
        for (const [key, value] of Object.entries(patch)) {
          if (key === "version") continue;
          if (key === "filesTreeIntent") {
            next[key] = mergePeerTreeIntents(getAppStateSnapshot().filesTreeIntent, value, `window:${thisLabel}`);
            continue;
          }
          if (value === null) delete next[key];
          else next[key] = value;
        }
        setAppStateSnapshot(parseAppState(next));
        applyDevicePrefsFromSnapshot();
      });
      if (disposed) {
        stop();
        return;
      }
      unlisten = stop;
    } catch {
      /* Preferences remain local when subscription fails. */
    }
  })();

  return () => {
    disposed = true;
    const stop = unlisten;
    unlisten = null;
    if (stop) stop();
  };
}
