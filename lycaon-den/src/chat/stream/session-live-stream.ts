import type { LycaonClient } from "../../api/client.ts";
import { parseSSEBuffer } from "../../api/events-sse.ts";
import { requireSuccessfulResponse } from "../../api/http.ts";
import type { Message, ToolCall } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { createLiveChunkBatch } from "./live-chunk-batch.ts";

export type LiveStreamChunk = {
  token?: string;
  reset?: boolean;
  tool_calls?: ToolCall[];
  done?: boolean;
};

export type PaintedLiveTarget = {
  sessionId: string;
  messageId: string;
  kind: "parent" | "worker";
  workerId?: string;
};

/** Trailing assistant row with status=streaming, if any. */
export function streamingMessageId(
  messages: readonly Message[] | undefined,
): string | undefined {
  if (!messages?.length) return undefined;
  for (let i = messages.length - 1; i >= 0; i--) {
    const row = messages[i]!;
    if (row.role !== "assistant") continue;
    if (row.status === "streaming") return row.id?.trim() || undefined;
  }
  return undefined;
}

export function parseLiveStreamChunk(raw: string): LiveStreamChunk | null {
  try {
    const parsed = JSON.parse(raw) as LiveStreamChunk;
    if (typeof parsed !== "object" || parsed === null) return null;
    return parsed;
  } catch {
    return null;
  }
}

export function applyLiveStreamChunkToMessage(
  existing: Message,
  chunk: LiveStreamChunk,
): Message {
  const next: Message = {
    ...existing,
    status: chunk.done ? "complete" : "streaming",
  };
  if (chunk.token != null) {
    next.content = chunk.reset ? chunk.token : existing.content + chunk.token;
  }
  if (chunk.tool_calls !== undefined) next.tool_calls = chunk.tool_calls;
  if (chunk.done) next.generating_tokens = undefined;
  return next;
}

/** Follow one session stream until done or abort; apply content in the store. */
export async function followSessionLiveStream(opts: {
  client: LycaonClient;
  appStore: AppStore;
  target: PaintedLiveTarget;
  signal: AbortSignal;
}): Promise<void> {
  const { client, appStore, target, signal } = opts;
  const sessionId = target.sessionId.trim();
  const messageId = target.messageId.trim();
  if (!sessionId || !messageId) return;

  if (signal.aborted) return;
  let res: Response;
  try {
    res = await requireSuccessfulResponse(
      await client.streamSession(sessionId, { messageId, signal }),
    );
  } catch (error) {
    if (signal.aborted) return;
    throw error;
  }
  if (!res.body) return;
  if (signal.aborted) {
    try {
      await res.body.cancel();
    } catch {
      /* ignore */
    }
    return;
  }

  const reader = res.body.getReader();
  const chunks = createLiveChunkBatch((chunk) => applyPaintedLiveChunk(appStore, target, chunk));
  const cancelReader = () => {
    chunks.cancel();
    void reader.cancel(signal.reason).catch(() => undefined);
  };
  signal.addEventListener("abort", cancelReader, { once: true });
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (!signal.aborted) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const parsed = parseSSEBuffer(buffer);
      buffer = parsed.rest;
      for (const msg of parsed.messages) {
        if (!msg.data) continue;
        const chunk = parseLiveStreamChunk(msg.data);
        if (!chunk) continue;
        chunks.push(chunk);
        if (chunk.done) return;
      }
    }
  } catch (error) {
    if (!signal.aborted) throw error;
  } finally {
    if (signal.aborted) chunks.cancel();
    else chunks.flush();
    signal.removeEventListener("abort", cancelReader);
    try {
      await reader.cancel(signal.reason);
    } catch {
      /* ignore */
    }
  }
}

// Settled rows ignore trailing live chunks.
function isSettled(message: Pick<Message, "status">): boolean {
  return message.status === "complete";
}

export function applyPaintedLiveChunk(
  appStore: AppStore,
  target: PaintedLiveTarget,
  chunk: LiveStreamChunk,
): void {
  if (target.kind === "worker" && target.workerId) {
    const rows = appStore.state.workerTranscripts[target.workerId]?.rows ?? [];
    const idx = rows.findIndex((m) => m.id === target.messageId);
    if (idx < 0) return;
    const existing = rows[idx]!;
    if (isSettled(existing)) return;
    const next = applyLiveStreamChunkToMessage(existing, chunk);
    appStore.actions.applyWorkerTranscriptRows(target.workerId, [next]);
    return;
  }
  if (appStore.state.transcriptSessionId !== target.sessionId) return;
  const existing = appStore.state.messages.find((m) => m.id === target.messageId);
  if (!existing) return;
  if (isSettled(existing)) return;
  appStore.actions.upsertMessage(applyLiveStreamChunkToMessage(existing, chunk));
}
