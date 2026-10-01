import { createEffect, createMemo, createSignal, on, onCleanup } from "solid-js";
import { agentColorStyle, readAgentPalette } from "../../contributions/agent-color-palette.ts";
import { type WindowColor } from "../../contributions/window-color-palette.ts";
import { watchPaletteTheme } from "../../contributions/palette-cache.ts";
import { editorShownAgentActivity } from "../../settings/editor/editor-prefs.ts";
import { MinVisibleHold } from "../../ui/min-visible-hold.ts";
import {
  AGENT_FILE_STATE_TEXT,
  agentFileKey,
  projectAgentFiles,
  type AgentChat,
  type AgentFilePresence,
  type AgentFileState,
} from "./agent-presence.ts";
import { agentPresenceFor } from "./agent-presence-store.ts";

/** Highest-priority activity and its owning chat. */
export type FilesAgentChip = { state: AgentFileState; text: string; description: string; sessionId: string };

export type FilesAgentName = { chat: AgentChat; labels: readonly string[] };

export type FilesAgentPresence = {
  chip: FilesAgentChip | null;
  name: FilesAgentName | null;
  /** Every agent fact about the file, for tooltips and accessible names. */
  labels: readonly string[];
};

function sameChip(a: FilesAgentChip, b: FilesAgentChip): boolean {
  return a.state === b.state && a.sessionId === b.sessionId && a.description === b.description;
}

function sameName(a: FilesAgentName, b: FilesAgentName): boolean {
  return a.chat.sessionId === b.chat.sessionId && a.chat.slot === b.chat.slot
    && a.labels.length === b.labels.length && a.labels.every((label, index) => label === b.labels[index]);
}

function chips(files: ReadonlyMap<string, AgentFilePresence>): Map<string, FilesAgentChip> {
  const out = new Map<string, FilesAgentChip>();
  for (const [key, file] of files) {
    if (!file.state || !file.stateChat) continue;
    out.set(key, {
      state: file.state,
      text: AGENT_FILE_STATE_TEXT[file.state],
      description: file.labels[0] ?? AGENT_FILE_STATE_TEXT[file.state],
      sessionId: file.stateChat.sessionId,
    });
  }
  return out;
}

function names(files: ReadonlyMap<string, AgentFilePresence>): Map<string, FilesAgentName> {
  const out = new Map<string, FilesAgentName>();
  for (const [key, file] of files) {
    if (file.highlight) out.set(key, { chat: file.highlight, labels: file.labels });
  }
  return out;
}

const [agentPalette, setAgentPalette] = createSignal<((slot: number) => WindowColor) | null>(null);
let paletteWatchers = 0;
let stopPaletteWatch: (() => void) | undefined;

function watchAgentPalette(): () => void {
  if (typeof document === "undefined") return () => undefined;
  const root = document.documentElement;
  if (paletteWatchers++ === 0) {
    setAgentPalette(() => readAgentPalette(root));
    stopPaletteWatch = watchPaletteTheme(root, () => setAgentPalette(() => readAgentPalette(root)));
  }
  return () => {
    if (--paletteWatchers > 0) return;
    stopPaletteWatch?.();
    stopPaletteWatch = undefined;
  };
}

/** Highlights follow palette changes. */
export function agentNameStyle(chat: AgentChat): Record<string, string> {
  return agentColorStyle(agentPalette()?.(chat.slot));
}

/** Holds activity chips and highlights long enough to prevent brief flashes. */
export function createFilesAgentPresence(options: {
  projectId: () => string;
  openSessionId: () => string | undefined;
}): (rootId: string, path: string) => FilesAgentPresence | null {
  const [shownChips, setShownChips] = createSignal<ReadonlyMap<string, FilesAgentChip>>(new Map());
  const [shownNames, setShownNames] = createSignal<ReadonlyMap<string, FilesAgentName>>(new Map());
  const chipHold = new MinVisibleHold<FilesAgentChip>(setShownChips, { same: sameChip });
  const nameHold = new MinVisibleHold<FilesAgentName>(setShownNames, { same: sameName });
  onCleanup(watchAgentPalette());
  onCleanup(() => {
    chipHold.dispose();
    nameHold.dispose();
  });
  createEffect(on(options.projectId, () => {
    chipHold.clear();
    nameHold.clear();
  }, { defer: true }));
  const files = createMemo(() => {
    const kinds = editorShownAgentActivity();
    return kinds.fileNames
      ? projectAgentFiles(agentPresenceFor(options.projectId()), kinds, options.openSessionId())
      : new Map<string, AgentFilePresence>();
  });
  createEffect(() => {
    chipHold.update(chips(files()));
    nameHold.update(names(files()));
  });
  return (rootId, path) => {
    const key = agentFileKey(rootId, path);
    const chip = shownChips().get(key) ?? null;
    const name = shownNames().get(key) ?? null;
    const labels = name?.labels ?? (chip ? [chip.description] : []);
    return chip || name ? { chip, name, labels } : null;
  };
}

export function agentChipClasses(state: AgentFileState): Record<string, boolean> {
  return {
    "den-files-presence--waiting": state === "waiting",
    "den-files-presence--editing": state === "editing" || state === "landing" || state === "ready",
    "den-files-presence--reading": state === "reading",
    "den-files-presence--sandbox": state === "sandbox",
  };
}
