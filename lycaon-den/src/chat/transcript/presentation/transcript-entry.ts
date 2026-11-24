import { createEffect, createSignal, onCleanup, untrack } from "solid-js";
import type { Message, TurnLoad } from "../../../api/types.ts";
import { verboseModePref } from "../../../settings/system/debug-prefs.ts";
import { partHasFileEdits } from "../../file-edit/file-edit-model.ts";
import { citationGroundingHadTraceableWork } from "../../grounding/citation-grounding-model.ts";
import { activitySpanToolParts } from "../../tool/activity-span-model.ts";
import {
  buildCanonicalArtifactPlacement,
  visualPresentEntryKey,
  visualProducerEntryKey,
} from "../../visual/visual-artifact-canonical.ts";
import {
  visualFromPart,
} from "../../visual/visual-artifact-model.ts";
import { finalizeTranscriptItems, messagesToTranscriptItems } from "../projection/transcript-items.ts";
import { type TranscriptItem } from "../projection/transcript-item-model.ts";
import { onTranscriptEnterFadeDone } from "./transcript-entry-fade.ts";
import { DEN_SCROLLING_ATTR } from "../../../platform/scrolling/scroll-activity.ts";

/** Entry keys prevent replaying arrival fades after hydration or remount. */
const ENTER_FADE_CLASS = "den-enter-fade";

export function activitySpanEnterFadeKey(rowKey: string): string {
	return `${rowKey.trim()}:activity-enter`;
}

export function groundingEvidenceEntryKey(rowKey: string): string {
  return `${rowKey.trim()}:evidence`;
}

export function workerGroupEnterFadeKey(rowKey: string): string {
	return `${rowKey.trim()}:worker-group`;
}

const MAX_TRANSIENT_LIVE_KEYS_PER_SESSION = 128;

const enteredKeysBySession = new Map<string, Set<string>>();
const hydratedSessions = new Set<string>();
const livePendingBySession = new Map<string, Set<string>>();
const recentLiveSettledBySession = new Map<string, Set<string>>();

function addBoundedKey(store: Map<string, Set<string>>, sid: string, key: string, max: number): void {
  let set = store.get(sid);
  if (!set) {
    set = new Set();
    store.set(sid, set);
  }
  if (set.has(key)) {
    set.delete(key);
  }
  set.add(key);
  while (set.size > max) {
    const oldest = set.values().next().value;
    if (oldest === undefined) break;
    set.delete(oldest);
  }
}

export function markTranscriptSessionHydrated(sessionId: string | undefined): void {
  const sid = sessionId?.trim();
  if (!sid) return;
  hydratedSessions.add(sid);
}

export function markTranscriptLiveEntries(
  sessionId: string | undefined,
  keys: readonly string[],
): void {
  const sid = sessionId?.trim();
  if (!sid || keys.length === 0) return;
  for (const raw of keys) {
    const key = raw.trim();
    if (!key) continue;
    if (recentLiveSettledBySession.get(sid)?.has(key)) continue;
    addBoundedKey(livePendingBySession, sid, key, MAX_TRANSIENT_LIVE_KEYS_PER_SESSION);
  }
}

export function transcriptEntryMemoryCount(sessionId: string): number {
  const sid = sessionId.trim();
  if (!sid) return 0;
  return (
    (enteredKeysBySession.get(sid)?.size ?? 0) +
    (livePendingBySession.get(sid)?.size ?? 0) +
    (recentLiveSettledBySession.get(sid)?.size ?? 0)
  );
}

export function rememberTranscriptEntry(
  sessionId: string | undefined,
  entryKey: string,
): void {
  const sid = sessionId?.trim() || "*";
  const key = entryKey.trim();
  if (!key) return;
  addBoundedKey(enteredKeysBySession, sid, key, MAX_TRANSIENT_LIVE_KEYS_PER_SESSION);
}

export function hasTranscriptEntryMemory(
  sessionId: string | undefined,
  entryKey: string,
): boolean {
  const sid = sessionId?.trim() || "*";
  const key = entryKey.trim();
  if (!key) return false;
  return (
    (enteredKeysBySession.get(sid)?.has(key) ?? false) ||
    (recentLiveSettledBySession.get(sid)?.has(key) ?? false)
  );
}

export function clearTranscriptEntryMemory(sessionId?: string): void {
  const sid = sessionId?.trim();
  if (!sid) {
    enteredKeysBySession.clear();
    hydratedSessions.clear();
    livePendingBySession.clear();
    recentLiveSettledBySession.clear();
    return;
  }
  enteredKeysBySession.delete(sid);
  hydratedSessions.delete(sid);
  livePendingBySession.delete(sid);
  recentLiveSettledBySession.delete(sid);
}

function collectToolPartEntryKeys(
  part: import("../../tool/tool-part-model.ts").ToolPartView,
  keys: Set<string>,
): void {
  keys.add(part.id);
  if (partHasFileEdits(part)) {
    keys.add(`${part.id}:file-edit`);
  }
  const visual = visualFromPart(part);
  if (visual?.id.trim()) {
    keys.add(visualProducerEntryKey(visual.id));
  }
}

function collectTranscriptEntryKeys(item: TranscriptItem, keys: Set<string>): void {
  const key = item.key.trim();
  if (!key) return;
  keys.add(key);

  switch (item.kind) {
    case "assistant":
      if (item.grounding && citationGroundingHadTraceableWork(item.grounding)) {
        keys.add(groundingEvidenceEntryKey(key));
      }
      break;
    case "tool":
      collectToolPartEntryKeys(item.part, keys);
      break;
    case "activity_span": {
      keys.add(activitySpanEnterFadeKey(key));
      // Fold identity comes from the first write, so diffs keep their part keys.
      for (const part of activitySpanToolParts(item.entries)) {
        collectToolPartEntryKeys(part, keys);
      }
      for (const entry of item.entries) {
        if (entry.kind === "turn_load") keys.add(entry.key);
      }
      break;
    }
    case "worker_group":
      keys.add(workerGroupEnterFadeKey(key));
      for (const part of item.parts) {
        collectToolPartEntryKeys(part, keys);
      }
      break;
    default:
      break;
  }
}

function collectMessageBackedEntryKeys(
  messages: readonly Message[],
  keys: Set<string>,
): void {
  for (const msg of messages) {
    if (msg.kind === "workflow_boundary" || msg.workflow_boundary) {
      keys.add(msg.id);
    }
    if (msg.role !== "assistant" && msg.role !== "user") continue;
    for (const id of msg.artifact_ids ?? []) {
      const trimmed = id.trim();
      if (trimmed) keys.add(visualPresentEntryKey(msg.id, trimmed));
    }
  }
  for (const placement of buildCanonicalArtifactPlacement(messages).values()) {
    keys.add(placement.entryKey);
  }
}

export function transcriptEntryKeysFromMessages(
  messages: readonly Message[],
  turnLoads?: Readonly<Record<string, readonly TurnLoad[]>>,
  sessionId?: string,
): string[] {
  const verboseMode = untrack(() => verboseModePref());
  const items = finalizeTranscriptItems(
    messagesToTranscriptItems(messages, {
      layout: "chat",
      turnLoads,
      sessionId,
      verboseMode,
    }),
    { layout: "chat", verboseMode },
  );
  const keys = new Set<string>();
  for (const item of items) collectTranscriptEntryKeys(item, keys);
  collectMessageBackedEntryKeys(messages, keys);
  return [...keys];
}

export function noteTranscriptEntryBaseline(
  sessionId: string,
  keys: readonly string[],
): void {
  const sid = sessionId.trim();
  if (!sid) return;
  for (const key of keys) rememberTranscriptEntry(sid, key);
}

export type TranscriptEntryBindOptions = {
  sessionId?: string;
  entryKey?: string;
  /** Record the key without playing the arrival fade. */
  skipFade?: boolean;
};

function retireEnterFade(node: HTMLElement): () => void {
  return onTranscriptEnterFadeDone(node, () => {
    node.classList.remove(ENTER_FADE_CLASS);
  });
}

function applyEnterFade(
  node: HTMLElement,
  sessionId: string | undefined,
  entryKey: string | undefined,
  skipFade = false,
): (() => void) | undefined {
  const sid = sessionId?.trim();
  const key = entryKey?.trim();
  const enteringThroughScroll =
    node.closest<HTMLElement>(`[${DEN_SCROLLING_ATTR}]`) !== null;

  if (sid && hydratedSessions.has(sid)) {
    const pending = key ? livePendingBySession.get(sid) : undefined;
    const isLiveArrival = Boolean(key && pending?.has(key));
    if (!isLiveArrival || skipFade || enteringThroughScroll) {
      if (key && pending?.delete(key)) {
        addBoundedKey(recentLiveSettledBySession, sid, key, MAX_TRANSIENT_LIVE_KEYS_PER_SESSION);
      }
      return undefined;
    }
    if (key) {
      pending!.delete(key);
      addBoundedKey(recentLiveSettledBySession, sid, key, MAX_TRANSIENT_LIVE_KEYS_PER_SESSION);
    }
    if (node.classList.contains(ENTER_FADE_CLASS)) return undefined;
    node.classList.add(ENTER_FADE_CLASS);
    return retireEnterFade(node);
  }

  const fresh = !key || !hasTranscriptEntryMemory(sessionId, key);
  if (key && !enteringThroughScroll) rememberTranscriptEntry(sessionId, key);
  if (!fresh || skipFade || enteringThroughScroll) return undefined;
  if (node.classList.contains(ENTER_FADE_CLASS)) return undefined;
  node.classList.add(ENTER_FADE_CLASS);
  return retireEnterFade(node);
}

export function useTranscriptEntry(
  opts?: () => TranscriptEntryBindOptions | undefined,
): {
  bindTranscriptEntry: (el: HTMLElement | undefined) => void;
} {
  const [node, setNode] = createSignal<HTMLElement>();
  let boundNode: HTMLElement | undefined;
  let bound: TranscriptEntryBindOptions | undefined;
  let cancelFade: (() => void) | undefined;

  // Effect disposal releases subscriptions even when refs lack a reactive context.
  createEffect(() => {
    const current = node();
    const options = opts?.();
    const next = {
      sessionId: options?.sessionId,
      entryKey: options?.entryKey?.trim(),
      skipFade: options?.skipFade === true,
    };
    if (current === boundNode && bound &&
      next.entryKey === bound.entryKey && next.sessionId === bound.sessionId &&
      next.skipFade === bound.skipFade) return;
    if (current !== boundNode) {
      cancelFade?.();
      cancelFade = undefined;
    }
    boundNode = current;
    bound = next;
    if (!current) return;
    const rearmed = applyEnterFade(current, next.sessionId, next.entryKey, next.skipFade);
    if (rearmed) cancelFade = rearmed;
  });
  onCleanup(() => cancelFade?.());
  const bindTranscriptEntry = (element: HTMLElement | undefined) => { setNode(element); };

  return { bindTranscriptEntry };
}
