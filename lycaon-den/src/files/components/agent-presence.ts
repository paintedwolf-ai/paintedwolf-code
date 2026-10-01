/** Projects host agent presence into file states and editor marks. */

import type {
  AgentIntent,
  AgentRead,
  AgentIntentOperation,
  AgentPresenceExtent,
  AgentSessionPresence,
  AgentTextRange,
  AgentWorkerDraft,
} from "../../api/types.ts";
import type { AgentActivityKinds } from "../../settings/editor/editor-prefs.ts";

/** One chat, with the palette slot every surface paints it in. */
export type AgentChat = { sessionId: string; title: string; slot: number };

export function agentFileKey(rootId: string, path: string): string {
  return `${rootId}\0${path}`;
}

const UNTITLED_CHAT = "Untitled chat";
/** Slots the palette separates before it wraps back through its lightness band. */
const DISTINCT_SLOTS = 7;
const REMEMBERED_SLOTS_MAX = 64;
const rememberedSlots = new Map<string, number>();
let chatsCache: {
  sessions: ReadonlyMap<string, AgentSessionPresence>;
  chats: ReadonlyMap<string, AgentChat>;
} | null = null;

function rememberSlot(sessionId: string, slot: number): void {
  rememberedSlots.delete(sessionId);
  rememberedSlots.set(sessionId, slot);
  while (rememberedSlots.size > REMEMBERED_SLOTS_MAX) {
    const oldest = rememberedSlots.keys().next().value;
    if (oldest === undefined) break;
    rememberedSlots.delete(oldest);
  }
}

/** Stable slots preserve chat colors across turns. */
export function agentChats(sessions: ReadonlyMap<string, AgentSessionPresence>): ReadonlyMap<string, AgentChat> {
  if (chatsCache?.sessions === sessions) return chatsCache.chats;
  const taken = new Set<number>();
  const slots = new Map<string, number>();
  const fresh: string[] = [];
  for (const sessionId of sessions.keys()) {
    const slot = rememberedSlots.get(sessionId);
    if (slot !== undefined && !taken.has(slot)) {
      taken.add(slot);
      slots.set(sessionId, slot);
    } else {
      fresh.push(sessionId);
    }
  }
  const heldByAbsent = new Set<number>();
  for (const [sessionId, slot] of rememberedSlots) {
    if (!sessions.has(sessionId)) heldByAbsent.add(slot);
  }
  for (const sessionId of fresh.sort()) {
    let slot = 0;
    while (taken.has(slot) || (heldByAbsent.has(slot) && slot < DISTINCT_SLOTS)) slot++;
    if (slot >= DISTINCT_SLOTS) {
      slot = 0;
      while (taken.has(slot)) slot++;
    }
    taken.add(slot);
    slots.set(sessionId, slot);
  }
  const chats = new Map<string, AgentChat>();
  for (const [sessionId, presence] of sessions) {
    const slot = slots.get(sessionId) ?? 0;
    rememberSlot(sessionId, slot);
    chats.set(sessionId, { sessionId, title: presence.title?.trim() || UNTITLED_CHAT, slot });
  }
  chatsCache = { sessions, chats };
  return chats;
}

/** The presence each surface shows under the user's settings. */
function shownAgentPresence(presence: AgentSessionPresence, kinds: AgentActivityKinds): AgentSessionPresence {
  return {
    ...presence,
    activities: presence.activities.filter((activity) =>
      activity.kind === "reading" ? kinds.reads : kinds.changes),
    reads: kinds.reads ? presence.reads : [],
    intents: kinds.changes ? presence.intents : [],
    worker_drafts: kinds.changes ? presence.worker_drafts : [],
  };
}

/** Chats in presentation order: the open chat first, then by slot. */
function orderedChats(
  sessions: ReadonlyMap<string, AgentSessionPresence>,
  openSessionId: string | null | undefined,
): Array<{ chat: AgentChat; presence: AgentSessionPresence }> {
  const chats = agentChats(sessions);
  return [...sessions]
    .flatMap(([sessionId, presence]) => {
      const chat = chats.get(sessionId);
      return chat ? [{ chat, presence, open: sessionId === openSessionId }] : [];
    })
    .sort((a, b) => Number(b.open) - Number(a.open) || a.chat.slot - b.chat.slot)
    .map(({ chat, presence }) => ({ chat, presence }));
}

/** Live file states in presentation priority order. */
export type AgentFileState = "waiting" | "editing" | "landing" | "ready" | "reading" | "sandbox" | "reserved";

const STATE_RANK: Record<AgentFileState, number> = {
  waiting: 0,
  editing: 1,
  landing: 2,
  ready: 3,
  reading: 4,
  sandbox: 5,
  reserved: 6,
};

export const AGENT_FILE_STATE_TEXT: Record<AgentFileState, string> = {
  waiting: "waiting",
  editing: "editing",
  landing: "landing",
  ready: "ready to land",
  reading: "reading",
  sandbox: "sandbox",
  reserved: "reserved",
};

export type AgentFilePresence = {
  state: AgentFileState | null;
  /** The chat whose state the chip shows. */
  stateChat: AgentChat | null;
  /** The chat whose involvement highlights the file name. */
  highlight: AgentChat | null;
  /** Every fact about the file, strongest first, for tooltips and accessible names. */
  labels: readonly string[];
};

const OPERATION_VERB: Record<AgentIntentOperation, string> = {
  edit: "edit",
  write: "write",
  create: "create",
  delete: "delete",
  move: "move",
};

function intentFact(intent: AgentIntent): string {
  const verb = OPERATION_VERB[intent.operation];
  return intent.state === "awaiting_approval" ? `waiting for your approval to ${verb}` : `about to ${verb}`;
}

function draftFact(draft: AgentWorkerDraft): string {
  switch (draft.state) {
    case "reserved":
      return "reserved by a worker";
    case "drafting":
      return "drafting in a worker sandbox";
    case "landing":
      return "landing a worker draft";
    case "ready": {
      const counts = draft.insertions != null && draft.deletions != null ? ` (+${draft.insertions} −${draft.deletions})` : "";
      return `worker draft ready to land${counts}`;
    }
  }
}

const DRAFT_STATE: Record<AgentWorkerDraft["state"], AgentFileState> = {
  reserved: "reserved",
  drafting: "sandbox",
  ready: "ready",
  landing: "landing",
};

type FileFact = { state: AgentFileState | null; label: string };

/** Projects every file an agent is working in or has read this turn. */
export function projectAgentFiles(
  sessions: ReadonlyMap<string, AgentSessionPresence>,
  kinds: AgentActivityKinds,
  openSessionId: string | null | undefined,
): Map<string, AgentFilePresence> {
  const facts = new Map<string, Array<FileFact & { chat: AgentChat; order: number }>>();
  let order = 0;
  const add = (rootId: string, path: string, chat: AgentChat, fact: FileFact) => {
    const key = agentFileKey(rootId, path);
    const list = facts.get(key) ?? [];
    if (list.some((existing) => existing.chat === chat && existing.label === fact.label)) return;
    list.push({ ...fact, chat, order: order++ });
    facts.set(key, list);
  };
  for (const { chat, presence: raw } of orderedChats(sessions, openSessionId)) {
    const presence = shownAgentPresence(raw, kinds);
    const name = (fact: string) => `${chat.title}: ${fact}`;
    const intentKeys = new Set(presence.intents.map((intent) => agentFileKey(intent.root_id, intent.path)));
    for (const intent of presence.intents) {
      add(intent.root_id, intent.path, chat, {
        state: intent.state === "awaiting_approval" ? "waiting" : "editing",
        label: name(intentFact(intent)),
      });
    }
    for (const activity of presence.activities) {
      if (activity.kind === "editing" && intentKeys.has(agentFileKey(activity.root_id, activity.path))) continue;
      add(activity.root_id, activity.path, chat, { state: activity.kind, label: name(activity.kind) });
    }
    for (const draft of presence.worker_drafts) {
      add(draft.root_id, draft.path, chat, { state: DRAFT_STATE[draft.state], label: name(draftFact(draft)) });
    }
    const readFiles = new Map<string, { rootId: string; path: string; stale: boolean }>();
    for (const read of presence.reads) {
      const key = agentFileKey(read.root_id, read.path);
      const seen = readFiles.get(key);
      readFiles.set(key, { rootId: read.root_id, path: read.path, stale: (seen?.stale ?? false) || read.stale });
    }
    for (const read of readFiles.values()) {
      add(read.rootId, read.path, chat, {
        state: null,
        label: name(read.stale ? "read this turn, changed since" : "read this turn"),
      });
    }
  }
  const files = new Map<string, AgentFilePresence>();
  for (const [key, list] of facts) {
    list.sort((a, b) =>
      (a.state ? STATE_RANK[a.state] : Number.POSITIVE_INFINITY) - (b.state ? STATE_RANK[b.state] : Number.POSITIVE_INFINITY)
      || a.order - b.order);
    const strongest = list[0]!.state ? list[0]! : null;
    const byOrder = [...list].sort((a, b) => a.order - b.order);
    files.set(key, {
      state: strongest?.state ?? null,
      stateChat: strongest?.chat ?? null,
      highlight: byOrder[0]!.chat,
      labels: list.map((fact) => fact.label),
    });
  }
  return files;
}

/** What an editor mark depicts. */
export type AgentMarkKind =
  /** Lines a read returned. */
  | "read"
  /** A search match the model saw. */
  | "match"
  /** A whole file a read returned; painted as a gutter tint. */
  | "whole"
  /** Lines a pending change will replace. */
  | "pending"
  /** Where a pending change inserts lines. */
  | "insertion"
  /** A whole file a pending change rewrites, deletes, or moves. */
  | "whole-pending";

export type AgentMarkSource =
  | { kind: "read"; item: AgentRead }
  | { kind: "intent"; item: AgentIntent }
  | { kind: "draft"; item: AgentWorkerDraft };

export type AgentMark = {
  source: AgentMarkSource;
  /** Stable across updates while the mark depicts the same item. */
  key: string;
  chat: AgentChat;
  kind: AgentMarkKind;
  stale: boolean;
  /** Absent for whole-file kinds. */
  range?: AgentTextRange;
  documentId?: string;
  epoch?: number;
  /** A persistent label naming why a change has not landed. */
  label?: string;
  /** The chat's newest read position carries its caret. */
  caret: boolean;
};

function isInsertion(range: AgentTextRange): boolean {
  return range.start_line === range.end_line
    && range.start_character !== undefined
    && range.start_character === range.end_character;
}

function changeMarks(
  base: { key: string; chat: AgentChat; source: AgentMarkSource; documentId?: string; epoch?: number },
  extent: AgentPresenceExtent,
  ranges: readonly AgentTextRange[],
  label: string,
): AgentMark[] {
  if (extent === "whole_file") {
    return [{ ...base, key: `${base.key}:whole`, kind: "whole-pending", stale: false, label, caret: false }];
  }
  return ranges.map((range, index) => ({
    ...base,
    key: `${base.key}:${index}`,
    kind: isInsertion(range) ? "insertion" : "pending",
    stale: false,
    range,
    caret: false,
    ...(index === 0 ? { label } : {}),
  }));
}

const DRAFT_LABEL: Partial<Record<AgentWorkerDraft["state"], string>> = {
  ready: "worker draft ready to land",
  landing: "landing a worker draft",
};

/** Marks one document shows, in paint order: reads, then changes. */
export function agentDocumentMarks(
  sessions: ReadonlyMap<string, AgentSessionPresence>,
  kinds: AgentActivityKinds,
  openSessionId: string | null | undefined,
  rootId: string,
  path: string,
): AgentMark[] {
  const marks: AgentMark[] = [];
  const here = (item: { root_id: string; path: string }) => item.root_id === rootId && item.path === path;
  for (const { chat, presence: raw } of orderedChats(sessions, openSessionId)) {
    const presence = shownAgentPresence(raw, kinds);
    const reads = presence.reads.filter(here).sort((a, b) => a.sequence - b.sequence);
    const readMarks: AgentMark[] = [];
    for (const read of reads) {
      if (read.extent === "whole_file") {
        readMarks.push({ key: `${read.id}:whole`, source: { kind: "read", item: read }, chat,
          kind: "whole", stale: read.stale, caret: false });
        continue;
      }
      read.ranges.forEach((range, index) => {
        readMarks.push({
          key: `${read.id}:${index}`,
          source: { kind: "read", item: read },
          chat,
          kind: read.extent === "matches" ? "match" : "read",
          stale: read.stale,
          range,
          documentId: read.document_id,
          epoch: read.epoch,
          caret: false,
        });
      });
    }
    const newest = readMarks[readMarks.length - 1];
    if (newest && newest.kind !== "whole") newest.caret = true;
    marks.push(...readMarks);
    for (const intent of presence.intents.filter(here)) {
      marks.push(...changeMarks(
        { key: intent.id, chat, source: { kind: "intent", item: intent }, documentId: intent.document_id, epoch: intent.epoch },
        intent.extent,
        intent.ranges,
        intentFact(intent),
      ));
    }
    for (const draft of presence.worker_drafts.filter(here)) {
      const label = DRAFT_LABEL[draft.state];
      if (!label || (draft.extent !== "whole_file" && draft.ranges.length === 0)) continue;
      marks.push(...changeMarks(
        { key: `${draft.worker_id}:draft`, chat, source: { kind: "draft", item: draft }, documentId: draft.document_id, epoch: draft.epoch },
        draft.extent,
        draft.ranges,
        label,
      ));
    }
  }
  return marks;
}
