import { createEffect, createSignal, onCleanup } from "solid-js";
import type { Message, TurnLoad } from "../../../api/types.ts";
import { fileEditFromPart } from "../../file-edit/file-edit-model.ts";
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

const enteredKeysBySession = new Map<string, Set<string>>();

export function rememberTranscriptEntry(
  sessionId: string | undefined,
  entryKey: string,
): void {
  const sid = sessionId?.trim() || "*";
  const key = entryKey.trim();
  if (!key) return;
  let seen = enteredKeysBySession.get(sid);
  if (!seen) {
    seen = new Set();
    enteredKeysBySession.set(sid, seen);
  }
  seen.add(key);
}

export function hasTranscriptEntryMemory(
  sessionId: string | undefined,
  entryKey: string,
): boolean {
  const sid = sessionId?.trim() || "*";
  const key = entryKey.trim();
  if (!key) return false;
  return enteredKeysBySession.get(sid)?.has(key) ?? false;
}

export function clearTranscriptEntryMemory(sessionId?: string): void {
  const sid = sessionId?.trim();
  if (!sid) {
    enteredKeysBySession.clear();
    return;
  }
  enteredKeysBySession.delete(sid);
}

function collectToolPartEntryKeys(
  part: import("../../tool/tool-part-model.ts").ToolPartView,
  keys: Set<string>,
): void {
  keys.add(part.id);
  if (fileEditFromPart(part)) {
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
): string[] {
  const items = finalizeTranscriptItems(
    messagesToTranscriptItems(messages, { layout: "chat", turnLoads }),
    { layout: "chat" },
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
  const fresh = !entryKey || !hasTranscriptEntryMemory(sessionId, entryKey);
  // Recording before paint prevents remounts from replaying the fade.
  if (entryKey) rememberTranscriptEntry(sessionId, entryKey);
  const enteringThroughScroll =
    node.closest<HTMLElement>(`[${DEN_SCROLLING_ATTR}]`) !== null;
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
