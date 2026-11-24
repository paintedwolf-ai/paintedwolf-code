import { batch, createEffect, createSignal, on, onCleanup } from "solid-js";
import { DEFAULT_SESSION_TITLE, type RecentSession } from "../../../shared/app-state-types.ts";
import { stageComposerMutation } from "../../chat/composer/composer-document-store.ts";
import { recordCreatedSessionInRecents } from "../../chat/session/session-lifecycle.ts";
import { launchChipsFromIdea } from "../../home/idea-attachments.ts";
import type { FrozenComposition } from "../../home/home-composition.ts";
import type { SessionMode } from "../../api/types.ts";
import { configureLaunchDriver, driveLaunch, haltLaunch, wakeLaunch } from "../../home/launch/home-launch-driver.ts";
import {
  fingerprintPath,
  forgetLaunch,
  homeLaunches,
  loadHomeLaunches,
  persistLaunch,
  refreshHomeLaunches,
  saveComposition,
  setForegroundLaunch,
  stageLaunchBytes,
  submitHomeLaunch,
  updateLaunch,
} from "../../home/launch/home-launches.ts";
import { boundChat, type LaunchRecord } from "../../home/launch/home-launch-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { RecentsStore } from "../../store/recents-store.ts";
import type { ShellStore } from "../../store/shell-store.ts";
import type { CompositionRecord } from "../../home/launch/home-launch-model.ts";

type Options = {
  shell: ShellStore;
  appStore: AppStore;
  recents: RecentsStore;
  showProjects: () => void;
  resumeSession: (row: RecentSession) => Promise<void>;
};

export type HomeLaunchController = ReturnType<typeof createHomeLaunchController>;

/**
 * Owns Home submissions in this window: it shows a submitted idea as its chat
 * at once, drives the host protocol behind it, and hands the conversation to
 * the bound chat when its first turn is admitted.
 */
export function createHomeLaunchController(options: Options) {
  const [restoredComposition, setRestoredComposition] = createSignal<CompositionRecord>();
  let loaded: Promise<void> | undefined;

  configureLaunchDriver({
    client: () => options.appStore.state.sidecarStatus === "connected" ? getLycaonClient() : null,
    onConnectionChange: (listener) => {
      let disposed = false;
      createEffect(on(() => options.appStore.state.sidecarStatus, () => { if (!disposed) listener(); }, { defer: true }));
      return () => { disposed = true; };
    },
    handoff: (launchId, chat, followUp) => {
      const showing = options.shell.state.launch?.launchId === launchId;
      haltLaunch(launchId);
      forgetLaunch(launchId);
      void recordCreatedSessionInRecents(options.recents, chat).catch(() => undefined);
      if (followUp) {
        void stageComposerMutation(chat, [], followUp).catch(() => undefined);
      }
      // Completion never takes focus from where the person went meanwhile.
      if (!showing) return;
      void options.resumeSession({ ...chat, title: DEFAULT_SESSION_TITLE });
    },
  });

  // A launch left for other work stays reachable from Home; relaunch reopens
  // the one that was showing.
  createEffect(on(() => options.shell.state.launch?.launchId, (launchId) => {
    setForegroundLaunch(launchId);
  }, { defer: true }));

  const load = () => {
    loaded ??= loadHomeLaunches().then(({ composition, foregroundLaunchId }) => {
      setRestoredComposition(composition);
      for (const record of homeLaunches.pending()) {
        if (record.intent !== "discard") driveLaunch(record.launchId);
      }
      if (
        foregroundLaunchId && homeLaunches.record(foregroundLaunchId) &&
        !options.shell.state.activeProjectId && !options.shell.state.launch
      ) {
        options.shell.openLaunch(foregroundLaunchId);
      }
    }, () => undefined);
    return loaded;
  };

  /** Turns the Home buffer into a foreground chat in one reactive update. */
  const submit = (draft: FrozenComposition & { mode: SessionMode }): string => {
    let launchId = "";
    batch(() => {
      launchId = submitHomeLaunch(
        {
          text: draft.text,
          mode: draft.mode,
          chips: launchChipsFromIdea(draft.attachments),
          compositionId: draft.compositionId,
          compositionRevision: draft.compositionRevision,
        },
        (winner) => {
          if (winner !== launchId && options.shell.state.launch?.launchId === launchId) {
            options.shell.openLaunch(winner);
          }
          driveLaunch(winner);
        },
      );
      options.showProjects();
      options.shell.openLaunch(launchId);
    });
    return launchId;
  };

  const open = (launchId: string) => {
    options.showProjects();
    options.shell.openLaunch(launchId);
    wakeLaunch(launchId);
  };

  /** Records Stop before canceling the transport. */
  const stop = (launchId: string) => {
    updateLaunch(launchId, (record) => ({ ...record, intent: "stop" }));
    haltLaunch(launchId);
    driveLaunch(launchId);
  };

  /** Removes the idea. A chat the host already created keeps its project. */
  const discard = (launchId: string) => {
    const record = homeLaunches.record(launchId);
    if (!record) return;
    haltLaunch(launchId);
    const client = getLycaonClient();
    if (record.hostState && client) void client.stopHomeStart(launchId).catch(() => undefined);
    forgetLaunch(launchId);
    options.shell.closeLaunch(launchId);
  };

  /** Puts the idea back on Home as an editable composition and removes the launch. */
  const editOnHome = (launchId: string) => {
    const record = homeLaunches.record(launchId);
    if (!record) return;
    const composition: CompositionRecord = {
      id: crypto.randomUUID(),
      revision: 1,
      text: record.proposedText ?? record.text,
      mode: record.mode,
      chips: record.chips,
      updatedAt: Date.now(),
    };
    void saveComposition(composition).catch(() => undefined);
    discard(launchId);
    setRestoredComposition(composition);
  };

  /** Sends the same idea as a new launch, leaving the old one behind. */
  const startAgain = (launchId: string) => {
    const record = homeLaunches.record(launchId);
    if (!record) return;
    discard(launchId);
    submitRecord(record);
  };

  const submitRecord = (record: LaunchRecord) => {
    let next = "";
    batch(() => {
      next = submitHomeLaunch(
        {
          text: record.proposedText ?? record.text,
          mode: record.mode,
          chips: record.chips,
          compositionId: crypto.randomUUID(),
          compositionRevision: 1,
        },
        (winner) => driveLaunch(winner),
      );
      options.shell.openLaunch(next);
    });
  };

  /** Edits the unsent idea; after an uncertain send the host fences the old one first. */
  const edit = (launchId: string, text: string) => {
    updateLaunch(launchId, (record) => record.sendAttempted
      ? { ...record, proposedText: text }
      : { ...record, text, firstPrompt: undefined });
    wakeLaunch(launchId);
  };

  const editChips = (launchId: string, change: (chips: LaunchRecord["chips"]) => LaunchRecord["chips"]) => {
    updateLaunch(launchId, (record) => ({ ...record, chips: change(record.chips), firstPrompt: undefined }));
    wakeLaunch(launchId);
  };

  const replaceChip = (launchId: string, itemId: string, file: File) => {
    const replacement = crypto.randomUUID();
    editChips(launchId, (chips) => chips.map((chip) => chip.itemId === itemId
      ? { kind: "bytes", itemId: replacement, name: file.name || chip.name, mime: file.type || "application/octet-stream", byteLength: file.size, snapshot: "pending" }
      : chip));
    void stageLaunchBytes(replacement, file).catch(() => undefined);
  };

  const acceptChangedFile = (launchId: string, itemId: string) => {
    const chip = homeLaunches.record(launchId)?.chips.find((c) => c.itemId === itemId);
    if (chip?.kind !== "path") return;
    void fingerprintPath(chip.absolutePath).then((fingerprint) => {
      editChips(launchId, (chips) => chips.map((c) => c.itemId === itemId && c.kind === "path"
        ? { ...c, ...(fingerprint ? { fingerprint } : {}), snapshot: "ready" }
        : c));
    });
  };

  const removeChip = (launchId: string, itemId: string) =>
    editChips(launchId, (chips) => chips.filter((chip) => chip.itemId !== itemId));

  /** Sends to the engine connected now; only before any host has seen the launch. */
  const sendHere = (launchId: string) => {
    const record = homeLaunches.record(launchId);
    if (!record) return;
    if (record.hostState) {
      startAgain(launchId);
      return;
    }
    updateLaunch(launchId, (r) => ({ ...r, destinationAccepted: true, host: undefined }));
    wakeLaunch(launchId);
  };

  const openChat = (launchId: string) => {
    const record = homeLaunches.record(launchId);
    const chat = record ? boundChat(record) : null;
    if (!chat) return;
    haltLaunch(launchId);
    forgetLaunch(launchId);
    void options.resumeSession({ ...chat, title: DEFAULT_SESSION_TITLE });
  };

  const refresh = () => { void refreshHomeLaunches().catch(() => undefined); };
  if (typeof window !== "undefined") {
    window.addEventListener("focus", refresh);
    onCleanup(() => window.removeEventListener("focus", refresh));
  }

  onCleanup(() => {
    for (const record of homeLaunches.pending()) haltLaunch(record.launchId);
  });

  return {
    load,
    restoredComposition,
    submit,
    open,
    stop,
    discard,
    retry: wakeLaunch,
    retrySaving: persistLaunch,
    edit,
    editOnHome,
    startAgain,
    replaceChip,
    acceptChangedFile,
    removeChip,
    sendHere,
    openChat,
  };
}
