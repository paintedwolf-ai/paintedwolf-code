import { createEffect, createMemo, createSignal, on, onCleanup } from "solid-js";
import { agentColorStyle, readAgentPalette } from "../../contributions/agent-color-palette.ts";
import { type WindowColor } from "../../contributions/window-color-palette.ts";
import { watchPaletteTheme } from "../../contributions/palette-cache.ts";
import { editorShownAgentActivity, type AgentActivityKinds } from "../../settings/editor/editor-prefs.ts";
import { Linger, type LingerPhase, type Lingered } from "../../ui/linger.ts";
import {
  AGENT_FILE_STATE_TEXT,
  AGENT_LINGER_MS,
  AGENT_RECENT_TEXT,
  agentFileKey,
  agentRecentKind,
  agentRecentLabel,
  projectAgentFiles,
  type AgentChat,
  type AgentFilePresence,
  type AgentFileState,
} from "./agent-presence.ts";
import { agentPresenceFor } from "./agent-presence-store.ts";

/** Highest-priority activity and its owning chat; past once that activity has ended. */
export type FilesAgentChip = {
  state: AgentFileState;
  phase: LingerPhase;
  text: string;
  description: string;
  sessionId: string;
};

export type FilesAgentName = { chat: AgentChat; phase: LingerPhase; labels: readonly string[] };

export type FilesAgentPresence = {
  chip: FilesAgentChip | null;
  name: FilesAgentName | null;
  /** Every agent fact about the file, for tooltips and accessible names. */
  labels: readonly string[];
};

type ChipFact = { state: AgentFileState; chat: AgentChat; description: string };
type NameFact = { chat: AgentChat; labels: readonly string[]; recent: AgentFilePresence["recent"] };

function sameChat(a: AgentChat, b: AgentChat): boolean {
  return a.sessionId === b.sessionId && a.slot === b.slot && a.title === b.title;
}

function sameChip(a: ChipFact, b: ChipFact): boolean {
  return a.state === b.state && sameChat(a.chat, b.chat) && a.description === b.description;
}

function sameName(a: NameFact, b: NameFact): boolean {
  const sameRecent = a.recent === null || b.recent === null
    ? a.recent === b.recent
    : a.recent.kind === b.recent.kind && sameChat(a.recent.chat, b.recent.chat);
  return sameChat(a.chat, b.chat) && sameRecent
    && a.labels.length === b.labels.length && a.labels.every((label, index) => label === b.labels[index]);
}

function chipFacts(files: ReadonlyMap<string, AgentFilePresence>): Map<string, ChipFact> {
  const out = new Map<string, ChipFact>();
  for (const [key, file] of files) {
    if (!file.state || !file.stateChat) continue;
    out.set(key, { state: file.state, chat: file.stateChat, description: file.labels[0] ?? AGENT_FILE_STATE_TEXT[file.state] });
  }
  return out;
}

function nameFacts(files: ReadonlyMap<string, AgentFilePresence>): Map<string, NameFact> {
  const out = new Map<string, NameFact>();
  for (const [key, file] of files) {
    if (file.highlight) out.set(key, { chat: file.highlight, labels: file.labels, recent: file.recent });
  }
  return out;
}

function shownChip({ value, phase }: Lingered<ChipFact>): FilesAgentChip {
  const recent = phase === "past" ? agentRecentKind(value.state) : null;
  return {
    state: value.state,
    phase,
    text: recent ? AGENT_RECENT_TEXT[recent] : AGENT_FILE_STATE_TEXT[value.state],
    description: recent ? agentRecentLabel(value.chat, recent) : value.description,
    sessionId: value.chat.sessionId,
  };
}

function shownName({ value, phase }: Lingered<NameFact>): FilesAgentName {
  const recent = phase === "past" ? value.recent : null;
  return { chat: value.chat, phase, labels: recent ? [agentRecentLabel(recent.chat, recent.kind)] : value.labels };
}

function sameKinds(a: AgentActivityKinds, b: AgentActivityKinds): boolean {
  return a.reads === b.reads && a.changes === b.changes && a.fileNames === b.fileNames;
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

/** Shows file activity as it is reported; editing and reading stay shown, as past, after they end. */
export function createFilesAgentPresence(options: {
  projectId: () => string;
  openSessionId: () => string | undefined;
}): (rootId: string, path: string) => FilesAgentPresence | null {
  const [shownChips, setShownChips] = createSignal<ReadonlyMap<string, FilesAgentChip>>(new Map());
  const [shownNames, setShownNames] = createSignal<ReadonlyMap<string, FilesAgentName>>(new Map());
  const chips = new Linger<ChipFact>(
    (shown) => setShownChips(new Map([...shown].map(([key, entry]) => [key, shownChip(entry)]))),
    { lingerMs: AGENT_LINGER_MS, same: sameChip, lingers: (fact) => agentRecentKind(fact.state) !== null },
  );
  const names = new Linger<NameFact>(
    (shown) => setShownNames(new Map([...shown].map(([key, entry]) => [key, shownName(entry)]))),
    { lingerMs: AGENT_LINGER_MS, same: sameName, lingers: (fact) => fact.recent !== null },
  );
  onCleanup(watchAgentPalette());
  onCleanup(() => {
    chips.dispose();
    names.dispose();
  });
  createEffect(on(options.projectId, () => {
    chips.clear();
    names.clear();
  }, { defer: true }));
  const files = createMemo(() => {
    const kinds = editorShownAgentActivity();
    return kinds.fileNames
      ? projectAgentFiles(agentPresenceFor(options.projectId()), kinds, options.openSessionId())
      : new Map<string, AgentFilePresence>();
  });
  let shownKinds: AgentActivityKinds | null = null;
  createEffect(() => {
    const kinds = editorShownAgentActivity();
    // Activity a person turns off goes at once instead of lingering.
    if (shownKinds && !sameKinds(shownKinds, kinds)) {
      chips.clear();
      names.clear();
    }
    shownKinds = kinds;
    chips.update(chipFacts(files()));
    names.update(nameFacts(files()));
  });
  return (rootId, path) => {
    const key = agentFileKey(rootId, path);
    const chip = shownChips().get(key) ?? null;
    const name = shownNames().get(key) ?? null;
    if (!chip && !name) return null;
    const labels = [
      ...(chip?.phase === "past" ? [chip.description] : []),
      ...(name?.labels ?? (chip ? [chip.description] : [])),
    ];
    return { chip, name, labels: [...new Set(labels)] };
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
