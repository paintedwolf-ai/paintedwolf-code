import { createEffect, createMemo, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { hasPromptInFlight } from "../session/session-activity.ts";
import {
  followSessionLiveStream,
  streamingMessageId,
  type PaintedLiveTarget,
} from "./session-live-stream.ts";

export type PaintInterestOpts = {
  appStore: AppStore;
  client: () => LycaonClient | null;
  sessionId: () => string | undefined;
  /** Paint-only subscriptions sleep with their resident surface. */
  active?: () => boolean;
  paintedWorker?: () =>
    | { workerId: string; childSessionId: string }
    | null
    | undefined;
  /** Skip subscribe while chat hydrate holds the lock. */
  hydrationLock?: () => string | undefined | null;
};

/** Subscribe to session streams for painted live drafts only. */
export function bindPaintInterestLiveStreams(opts: PaintInterestOpts): void {
  const targets = createMemo(
    () => paintedLiveTargets(opts),
    [] as PaintedLiveTarget[],
    { equals: samePaintedLiveTargets },
  );

  createEffect(() => {
    const client = opts.client();
    if (!client) return;
    const currentTargets = targets();
    if (currentTargets.length === 0) return;

    const ac = new AbortController();
    for (const target of currentTargets) {
      void followSessionLiveStream({
        client,
        appStore: opts.appStore,
        target,
        signal: ac.signal,
      }).catch(() => undefined);
    }
    onCleanup(() => ac.abort());
  });
}

function paintedLiveTargets(opts: PaintInterestOpts): PaintedLiveTarget[] {
  if (opts.active?.() === false) return [];
  if (opts.hydrationLock?.()?.trim()) return [];
  const targets: PaintedLiveTarget[] = [];
  const parentSessionId = opts.sessionId()?.trim();
  if (parentSessionId) {
    const busy =
      opts.appStore.state.currentSession?.id === parentSessionId &&
      (opts.appStore.state.currentSession.status === "busy" ||
        opts.appStore.state.sessionActivity[parentSessionId]?.llmTurn?.status ===
          "active" ||
        hasPromptInFlight(opts.appStore.state.sessionActivity[parentSessionId]));
    if (busy) {
      const messageId = streamingMessageId(opts.appStore.state.messages);
      if (messageId) {
        targets.push({
          sessionId: parentSessionId,
          messageId,
          kind: "parent",
        });
      }
    }
  }

  const worker = opts.paintedWorker?.();
  if (worker?.childSessionId && worker.workerId) {
    const rows = opts.appStore.state.workerTranscripts[worker.workerId]?.rows;
    const messageId = streamingMessageId(rows);
    if (messageId) {
      targets.push({
        sessionId: worker.childSessionId,
        messageId,
        kind: "worker",
        workerId: worker.workerId,
      });
    }
  }
  return targets;
}

function samePaintedLiveTargets(
  left: readonly PaintedLiveTarget[],
  right: readonly PaintedLiveTarget[],
): boolean {
  return (
    left.length === right.length &&
    left.every((target, index) => {
      const other = right[index];
      return (
        other !== undefined &&
        target.sessionId === other.sessionId &&
        target.messageId === other.messageId &&
        target.kind === other.kind &&
        target.workerId === other.workerId
      );
    })
  );
}
